package dequeuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs"
	"sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/prost"
	"sequencer-dequeuer/pkgs/redis"
	"sequencer-dequeuer/pkgs/reporting"
	"sequencer-dequeuer/pkgs/utils"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
)

// TODO: Submit Snapshots at scale
var SubmissionHandlerInstance *SubmissionHandler

// SubmissionDetails encapsulates the data needed for processing a submission
type SubmissionDetails struct {
	submissionId uuid.UUID
	submission   *pkgs.SnapshotSubmission
}

type SubmissionHandler struct {
}

func StartSubmissionHandler() {
	day, err := prost.MustQuery[*big.Int](context.Background(), func() (*big.Int, error) {
		return prost.Instance.DayCounter(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress)
	})
	if err != nil {
		// Contract query failure - reported by MustQuery
		log.Errorln("Encountered error while querying dayCounter: ", err.Error())
	}
	// TODO: Move to state manager
	err = redis.Set(context.Background(), pkgs.SequencerDayKey, day.String(), 0)
	if err != nil {
		//TODO: Report on slack
		return
	}
	SubmissionHandlerInstance = &SubmissionHandler{}
	go SubmissionHandlerInstance.startSubmissionDequeuer()
}

type SnapshotData struct {
	EpochID     uint64
	SlotID      uint64
	Deadline    uint64
	SnapshotCid string
	ProjectID   string
	Timestamp   int64
}

func isFullNode(addr string) bool {
	for _, node := range config.SettingsObj.FullNodes {
		if node == addr {
			return true
		}
	}
	return false
}

// TODO: Update verification logic
func (s *SubmissionHandler) verifyAndStoreSubmission(details SubmissionDetails) error {
	snapshotterAddr, err := utils.RecoverAddress(utils.HashRequest(details.submission.Request), common.Hex2Bytes(details.submission.Signature))
	// TODO: This can be an incorrect submission altogether - check need for reporting
	if err != nil {
		log.Errorln("Unable to recover snapshotter address: ", err.Error())
		return errors.New(fmt.Sprintf("Snapshotter address recovery error: %s", err.Error()))
	}

	if details.submission.Request.EpochId == 0 {
		log.Debugln("Received simulated submission: ", details.submission.String())

		var address common.Address
		retrr := backoff.Retry(func() error {
			address, err = prost.Instance.SlotSnapshotterMapping(&bind.CallOpts{}, new(big.Int).SetUint64(details.submission.Request.SlotId))
			return err
		}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5))

		if retrr != nil {
			log.Errorln("Unable to query SlotSnapshotterMapping: ", retrr.Error())
			return retrr
		}

		if address.Hex() != snapshotterAddr.Hex() {
			log.Errorln("Incorrect snapshotter address: ", snapshotterAddr.Hex())
			return errors.New(fmt.Sprintf("Snapshotter address not the one configured in slot: %s", snapshotterAddr.Hex()))
		} else {
			//log.Debugf("Snapshotter %s passed validation check!", snapshotterAddr.Hex())
		}

		key := redis.GetSnapshotterSubmissionCountInSlot(snapshotterAddr.Hex(), new(big.Int).SetUint64(details.submission.Request.SlotId))
		err = redis.RedisClient.Incr(context.Background(), key).Err()
		if err != nil {
			log.Errorf("Failed to increment in Redis: %v", err.Error())
			return errors.New(fmt.Sprintf("Redis client failure: %s", err.Error()))
		}
		projectData := strings.Split(details.submission.Request.ProjectId, "|")
		var projectIDFormatted string
		var nodeVersion string

		if len(projectData) == 1 {
			projectIDFormatted = projectData[0]
			nodeVersion = "v0.0.0"
		} else {
			projectIDFormatted = strings.Join(projectData[:len(projectData)-1], "|")
			nodeVersion = projectData[len(projectData)-1]
		}

		key = redis.GetSnapshotterNodeVersion(snapshotterAddr.Hex(), new(big.Int).SetUint64(details.submission.Request.SlotId))

		// Setting the node version in Redis
		err = redis.RedisClient.Set(context.Background(), key, nodeVersion, 0).Err()
		if err != nil {
			log.Errorf("Failed to set node version in Redis: %v", err)
			return errors.New(fmt.Sprintf("Redis client failure: %s", err.Error()))
		}

		data := SnapshotData{
			EpochID:     details.submission.Request.EpochId,
			SlotID:      details.submission.Request.SlotId,
			Deadline:    details.submission.Request.Deadline,
			SnapshotCid: details.submission.Request.SnapshotCid,
			ProjectID:   projectIDFormatted,
			Timestamp:   time.Now().Unix(),
		}

		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Errorf("Error serializing data: %v", err)
			return errors.New(fmt.Sprintf("json marshalling error: %s", err.Error()))
		}

		key = redis.GetSnapshotterSlotSubmissionsHtable(snapshotterAddr.Hex(), new(big.Int).SetUint64(details.submission.Request.SlotId))
		if err := redis.RedisClient.HSet(context.Background(), key, details.submission.Request.EpochId, jsonData).Err(); err != nil {
			log.Errorf("Failed to write to Redis: %v", err)
			return errors.New(fmt.Sprintf("Redis client failure: %s", err.Error()))
		}

		key = fmt.Sprintf("lastPing:%s:%s", snapshotterAddr.Hex(), strconv.Itoa(int(details.submission.Request.SlotId)))

		redis.RedisClient.Set(context.Background(), key, time.Now().Unix(), 0)

		return nil
	}

	if !isFullNode(snapshotterAddr.Hex()) {
		// TODO: Merge state changes

		slotInfoStr, err := redis.Get(context.Background(), redis.SlotInfo(strconv.FormatUint(details.submission.Request.SlotId, 10)))
		if err != nil || slotInfoStr == "" {
			reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprintf("Could not fetch slot info from cache: %s", err.Error()), time.Now().String(), "High")
			log.Errorln("Could not fetch slot info from cache: ", err.Error())
			return err
		} else {
			var slotInfo contract.PowerloomDataMarketSlotInfo
			err = json.Unmarshal([]byte(slotInfoStr), &slotInfo)
			if err != nil {
				log.Errorln("Unable to unmarshal slotInfo: ", slotInfoStr)
				reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprintf("Unable to unmarshal slotInfo: %s", slotInfoStr), time.Now().String(), "High")
				return err
			}

			// AllSnapshotters state check to be added
			var errMsg string
			if snapshotterAddr.Hex() != slotInfo.SnapshotterAddress.Hex() {
				errMsg = "Incorrect snapshotter address extracted" + string(snapshotterAddr.Hex()) + "for specified slot " + strconv.FormatUint(details.submission.Request.SlotId, 10) + " : " + string(slotInfo.SnapshotterAddress.Hex())
			} else {
				exists, err := redis.RedisClient.Exists(context.Background(), redis.SlotEpochSubmissionCountExceeded(strconv.FormatUint(details.submission.Request.SlotId, 10), details.submission.Request.EpochId)).Result()
				if err != nil {
					log.Errorf("Failed to check if slot epoch submission count exceeded exists: %v", err)
					return fmt.Errorf("redis client failure: %s", err.Error())
				} else if exists > 0 {
					errMsg = fmt.Sprintf("Slot epoch submission count exceeded for slot ID %s and epoch ID %d", strconv.FormatUint(details.submission.Request.SlotId, 10), details.submission.Request.EpochId)
					log.Errorln("Slot epoch submission count exceeded: ", errMsg)
					return errors.New(errMsg)
				}
				slotEpochCounterKey := redis.SlotEpochSubmissionsKey(strconv.FormatUint(details.submission.Request.SlotId, 10), details.submission.Request.EpochId)
				count, err := redis.Incr(context.Background(), slotEpochCounterKey)
				if err != nil {
					log.Errorf("Failed to increment slot epoch counter: %v", err)
					return fmt.Errorf("redis client failure: %s", err.Error())
				} else {
					if count > 2 {
						errMsg := fmt.Sprintf("Slot epoch submission count exceeded for slot ID %s", strconv.FormatUint(details.submission.Request.SlotId, 10))
						log.Errorln("Slot epoch submission count exceeded: ", errMsg)
						redis.Set(
							context.Background(),
							redis.SlotEpochSubmissionCountExceeded(strconv.FormatUint(details.submission.Request.SlotId, 10), details.submission.Request.EpochId),
							"true",
							5*time.Minute,
						)
						return errors.New(errMsg)
					}
				}
				currentEpochStr, _ := redis.Get(context.Background(), pkgs.CurrentEpoch)
				if currentEpochStr == "" {
					reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprintf("Current epochId not stored in redis: %s", err.Error()), time.Now().String(), "High")
					log.Errorf("Current epochId not stored in redis: %s", err.Error())
				} else {
					currentEpoch, err := strconv.Atoi(currentEpochStr)
					if err != nil {
						reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprintf("Cannot parse epoch %s stored in redis: %s", currentEpochStr, err.Error()), time.Now().String(), "High")
						log.Errorf("Cannot parse epoch %s stored in redis: %s", currentEpochStr, err.Error())
					} else if diff := uint64(currentEpoch) - details.submission.Request.EpochId; diff < 0 || diff > 1 {
						errMsg = "Incorrect epochId supplied in request"
					}
				}
			}
			if errMsg != "" {
				log.Debugln("Snapshot submission rejected: ", errMsg)
				return errors.New("Invalid snapshot")
			}
		}
	}

	key := redis.SubmissionKey(details.submission.Request.EpochId, details.submission.Request.ProjectId, new(big.Int).SetUint64(details.submission.Request.SlotId).String())
	value := fmt.Sprintf("%s.%s", details.submissionId.String(), protojson.Format(details.submission))
	set := redis.SubmissionSetByHeaderKey(details.submission.Request.EpochId, details.submission.Header) //fmt.Sprintf("%s.%d.%s", CollectorKey, submission.Request.EpochId, submission.Header)

	if val, _ := redis.Get(context.Background(), key); val != "" {
		log.Debugln("Submission already exists: ", val)
		return nil
	}
	if err := redis.SetSubmission(context.Background(), key, value, set, 20*time.Minute); err != nil {
		log.Errorln("Error setting key-value pair: ", err.Error())
		return err
	}

	log.Debugf("Successfully set submission with set %s and key %s", set, key)

	// expire submissions set by header key
	if err := redis.RedisClient.Expire(context.Background(), set, 30*time.Minute).Err(); err != nil {
		log.Errorf("Failed to set expiry for submission set by header key: %v", err)
		// send notification
		reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprintf("Failed to set expiry for submission set by header key: %v", err), time.Now().String(), "High")
		return fmt.Errorf("redis client failure: %s", err.Error())
	}

	// Store increment submission count for this epoch
	err = redis.RedisClient.Incr(context.Background(), redis.EpochSubmissionsCount(details.submission.Request.EpochId)).Err()
	if err != nil {
		reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprintf("Error incrementing epochsubmissions: %v", err), time.Now().String(), "High")
		log.Errorf("Error incrementing epochsubmissions: %v", err)
	}

	// Add submissions to epochSubmissions HTable
	submissionJSON, err := json.Marshal(details.submission)
	if err != nil {
		log.Errorf("Error serializing submission: %v", err)
		return fmt.Errorf("json marshalling error: %s", err.Error())
	}

	epochKey := redis.EpochSubmissionsKey(details.submission.Request.EpochId)
	if err := redis.RedisClient.HSet(context.Background(), epochKey, details.submissionId.String(), submissionJSON).Err(); err != nil {
		log.Errorf("Failed to write submission details to Redis: %v", err)
		return fmt.Errorf("redis client failure: %s", err.Error())
	}
	if err := redis.RedisClient.Expire(context.Background(), epochKey, 30*time.Minute).Err(); err != nil {
		log.Errorf("Failed to set expiry for epoch submissions hash table %s: %v", epochKey, err)
		return fmt.Errorf("redis client failure: %s", err.Error())
	}
	return nil
}

func (s *SubmissionHandler) startSubmissionDequeuer() {
	log.Debugln("SubmissionHandler started")
	for {
		// Fetch from Redis queue
		result, err := redis.RedisClient.BRPop(context.Background(), 0, "submissionQueue").Result()
		if err != nil {
			log.Errorln("Error fetching from Redis queue:", err)
			continue
		}

		if len(result) < 2 {
			log.Errorln("Invalid data fetched from Redis queue")
			continue
		}

		var queueData map[string]interface{}
		err = json.Unmarshal([]byte(result[1]), &queueData)
		log.Debugln("Received data: ", result)
		log.Debugln("Unmarshalled data: ", queueData)
		if err != nil {
			log.Errorln("Error unmarshalling queue data:", err)
			continue
		}

		submissionIdStr, ok := queueData["submission_id"].(string)
		if !ok {
			log.Errorln("Invalid submission_id format in queue data")
			continue
		}
		submissionId := uuid.New()
		err = submissionId.UnmarshalText([]byte(submissionIdStr))
		if err != nil {
			log.Errorln("Error parsing submission_id:", err)
			continue
		}

		submissionStr, ok := queueData["data"].(string)
		if !ok {
			log.Errorln("Invalid data format in queue data")
			continue
		}

		var submission pkgs.SnapshotSubmission
		err = json.Unmarshal([]byte(submissionStr), &submission)
		if err != nil {
			log.Errorln("Error unmarshalling submission data:", err)
			continue
		}

		submissionDetails := SubmissionDetails{
			submissionId: submissionId,
			submission:   &submission,
		}

		log.Debugln("Submission received for verification and storage:", submissionDetails.submissionId.String())
		err = s.verifyAndStoreSubmission(submissionDetails)
		if err != nil {
			log.Debugln("VerifyAndStore error:", err.Error())
		}
	}
}
