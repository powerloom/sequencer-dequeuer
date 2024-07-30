package dequeuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"math/big"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs"
	"sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/prost"
	"sequencer-dequeuer/pkgs/redis"
	"sequencer-dequeuer/pkgs/utils"
	"strconv"
	"strings"
	"time"
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
	day, err := prost.MustQuery[*big.Int](context.Background(), prost.Instance.DayCounter)
	if err != nil {
		// Contract query failure - reported by MustQuery
		log.Errorln("Encountered error while querying dayCounter: ", err.Error())
	}
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
	ps := contract.PowerloomProtocolStateRequest{
		SlotId:      new(big.Int).SetUint64(details.submission.Request.SlotId),
		Deadline:    new(big.Int).SetUint64(details.submission.Request.Deadline),
		SnapshotCid: details.submission.Request.SnapshotCid,
		EpochId:     new(big.Int).SetUint64(details.submission.Request.EpochId),
		ProjectId:   details.submission.Request.ProjectId,
	}

	snapshotterAddr, err := utils.RecoverAddress(utils.HashRequest(details.submission.Request), common.Hex2Bytes(details.submission.Signature))
	// TODO: This can be an incorrect submission altogether - check need for reporting
	if err != nil {
		log.Errorln("Unable to recover snapshotter address: ", err.Error())
		return errors.New(fmt.Sprintf("Snapshotter address recovery error: %s", err.Error()))
	}

	if ps.EpochId == nil || ps.EpochId.Cmp(big.NewInt(0)) == 0 {
		log.Debugln("Received simulated submission: ", details.submission.String())

		var address common.Address
		retrr := backoff.Retry(func() error {
			address, err = prost.Instance.SlotSnapshotterMapping(&bind.CallOpts{}, ps.SlotId)
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

		key := redis.GetSnapshotterSubmissionCountInSlot(snapshotterAddr.Hex(), ps.SlotId)
		err = redis.RedisClient.Incr(context.Background(), key).Err()
		if err != nil {
			log.Errorf("Failed to increment in Redis: %v", err.Error())
			return errors.New(fmt.Sprintf("Redis client failure: %s", err.Error()))
		}
		projectData := strings.Split(ps.ProjectId, "|")
		var projectIDFormatted string
		var nodeVersion string

		if len(projectData) == 1 {
			projectIDFormatted = projectData[0]
			nodeVersion = "v0.0.0"
		} else {
			projectIDFormatted = strings.Join(projectData[:len(projectData)-1], "|")
			nodeVersion = projectData[len(projectData)-1]
		}

		key = redis.GetSnapshotterNodeVersion(snapshotterAddr.Hex(), ps.SlotId)

		// Setting the node version in Redis
		err = redis.RedisClient.Set(context.Background(), key, nodeVersion, 0).Err()
		if err != nil {
			log.Errorf("Failed to set node version in Redis: %v", err)
			return errors.New(fmt.Sprintf("Redis client failure: %s", err.Error()))
		}

		data := SnapshotData{
			EpochID:     ps.EpochId.Uint64(),
			SlotID:      ps.SlotId.Uint64(),
			Deadline:    ps.Deadline.Uint64(),
			SnapshotCid: ps.SnapshotCid,
			ProjectID:   projectIDFormatted,
			Timestamp:   time.Now().Unix(),
		}

		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Errorf("Error serializing data: %v", err)
			return errors.New(fmt.Sprintf("json marshalling error: %s", err.Error()))
		}

		key = redis.GetSnapshotterSlotSubmissionsHtable(snapshotterAddr.Hex(), ps.SlotId)
		if err := redis.RedisClient.HSet(context.Background(), key, ps.EpochId.Uint64(), jsonData).Err(); err != nil {
			log.Errorf("Failed to write to Redis: %v", err)
			return errors.New(fmt.Sprintf("Redis client failure: %s", err.Error()))
		}

		key = fmt.Sprintf("lastPing:%s:%s", snapshotterAddr.Hex(), ps.SlotId.String())

		redis.RedisClient.Set(context.Background(), key, time.Now().Unix(), 0)

		return nil
	}

	if !isFullNode(snapshotterAddr.Hex()) {
		if ok, err := prost.MustQuery[bool](
			context.Background(),
			func(opts *bind.CallOpts) (bool, error) {
				return prost.Instance.AcceptSnapshot(&bind.CallOpts{}, new(big.Int).SetUint64(details.submission.Request.SlotId), details.submission.Request.SnapshotCid, new(big.Int).SetUint64(details.submission.Request.EpochId), details.submission.Request.ProjectId, ps, common.Hex2Bytes(details.submission.Signature))
			},
		); err != nil {
			log.Errorln("Error verifying snapshot submission: ", err.Error())
			return err
		} else if !ok {
			log.Debugln("Snapshot submission rejected: ", details.submissionId.String())
			return errors.New("Invalid snapshot")
		}
	}

	key := redis.SubmissionKey(details.submission.Request.EpochId, details.submission.Request.ProjectId, new(big.Int).SetUint64(details.submission.Request.SlotId).String())
	value := fmt.Sprintf("%s.%s", details.submissionId.String(), details.submission.String())
	set := redis.SubmissionSetByHeaderKey(details.submission.Request.EpochId, details.submission.Header) //fmt.Sprintf("%s.%d.%s", CollectorKey, submission.Request.EpochId, submission.Header)

	if val, _ := redis.Get(context.Background(), key); val != "" {
		log.Debugln("Submission already exists: ", val)
		return nil
	}
	if err := redis.SetSubmission(context.Background(), key, value, set, 5*time.Minute); err != nil {
		log.Errorln("Error setting key-value pair: ", err.Error())
		return err
	}

	log.Debugf("Successfully set submission with set %s and key %s", set, key)

	// Store increment submission count for this epoch
	// TODO: This can be a redis failure, that is critical but need to evaluate next steps - ideally just report and move on
	if val, _ := redis.Get(context.Background(), redis.EpochSubmissionsCount(details.submission.Request.EpochId)); val != "" {
		count, _ := strconv.Atoi(val)
		count += 1
		redis.Set(context.Background(), redis.EpochSubmissionsCount(details.submission.Request.EpochId), strconv.Itoa(count), 4*time.Hour)
	} else {
		redis.Set(context.Background(), redis.EpochSubmissionsCount(details.submission.Request.EpochId), "1", 4*time.Hour)
	}

	// Add submissions to epochSubmissions HTable
	submissionJSON, err := json.Marshal(details.submission)
	if err != nil {
		log.Errorf("Error serializing submission: %v", err)
		return errors.New(fmt.Sprintf("json marshalling error: %s", err.Error()))
	}

	epochKey := redis.EpochSubmissionsKey(details.submission.Request.EpochId)
	if err := redis.RedisClient.HSet(context.Background(), epochKey, details.submissionId.String(), submissionJSON).Err(); err != nil {
		log.Errorf("Failed to write submission details to Redis: %v", err)
		return errors.New(fmt.Sprintf("Redis client failure: %s", err.Error()))
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
