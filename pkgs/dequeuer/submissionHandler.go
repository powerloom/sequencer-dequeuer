package dequeuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs"
	protocolStateABIGen "sequencer-dequeuer/pkgs/contract"
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

var SubmissionHandlerInstance *SubmissionHandler

// SubmissionDetails encapsulates the data needed for processing a submission
type SubmissionDetails struct {
	submissionId      uuid.UUID
	submission        *pkgs.SnapshotSubmission
	dataMarketAddress string
}

type SubmissionHandler struct {
}

func (sh *SubmissionHandler) Start() {
	// Implement the submission handling logic here
	sh.startSubmissionDequeuer()
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
	// TODO: check whether the data market address is valid
	snapshotterAddr, err := utils.RecoverAddress(utils.HashRequest(details.submission.Request), common.Hex2Bytes(details.submission.Signature))
	// TODO: This can be an incorrect submission altogether - check need for reporting
	if err != nil {
		log.Errorln("Unable to recover snapshotter address: ", err.Error())
		return fmt.Errorf("snapshotter address recovery error: %s", err.Error())
	}

	// Verify if the snapshotter address is included in the set of flagged accounts in Redis
	flaggedSnapshotterKey := redis.FlaggedSnapshotterKey(details.dataMarketAddress)
	isFlagged, err := redis.RedisClient.SIsMember(context.Background(), flaggedSnapshotterKey, snapshotterAddr.Hex()).Result()
	if err != nil {
		log.Errorf("Error querying Redis for flagged snapshotters for data market %s: %v", details.dataMarketAddress, err)
		return fmt.Errorf("redis query error: %s", err.Error())
	}

	if isFlagged {
		log.Debugf("Submission from flagged snapshotter for data market %s: %s", details.dataMarketAddress, snapshotterAddr.Hex())
		return fmt.Errorf("snapshot submission rejected: snapshotter %s is flagged", snapshotterAddr.Hex())
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
			return fmt.Errorf("snapshotter address not the one configured in slot: %s", snapshotterAddr.Hex())
		}

		key := redis.GetSnapshotterSubmissionCountInSlot(details.dataMarketAddress, snapshotterAddr.Hex(), new(big.Int).SetUint64(details.submission.Request.SlotId))
		err = redis.RedisClient.Incr(context.Background(), key).Err()
		if err != nil {
			log.Errorf("Failed to increment in Redis: %v", err.Error())
			return fmt.Errorf("redis client failure: %s", err.Error())
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

		key = redis.GetSnapshotterNodeVersion(details.dataMarketAddress, snapshotterAddr.Hex(), new(big.Int).SetUint64(details.submission.Request.SlotId))

		// Setting the node version in Redis
		err = redis.RedisClient.Set(context.Background(), key, nodeVersion, 0).Err()
		if err != nil {
			log.Errorf("Failed to set node version in Redis: %v", err)
			return fmt.Errorf("redis client failure: %s", err.Error())
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
			return fmt.Errorf("json marshalling error: %s", err.Error())
		}

		key = redis.GetSnapshotterSlotSubmissionsHtable(details.dataMarketAddress, snapshotterAddr.Hex(), new(big.Int).SetUint64(details.submission.Request.SlotId))
		if err := redis.RedisClient.HSet(context.Background(), key, details.submission.Request.EpochId, jsonData).Err(); err != nil {
			log.Errorf("Failed to write to Redis: %v", err)
			return fmt.Errorf("redis client failure: %s", err.Error())
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
			var slotInfo protocolStateABIGen.PowerloomDataMarketSlotInfo
			err = json.Unmarshal([]byte(slotInfoStr), &slotInfo)
			if err != nil {
				log.Errorln("Unable to unmarshal slotInfo: ", slotInfoStr)
				reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprintf("Unable to unmarshal slotInfo: %s", slotInfoStr), time.Now().String(), "High")
				return err
			}

			// AllSnapshotters state check to be added
			var errMsg string
			if snapshotterAddr.Hex() != slotInfo.SnapshotterAddress.Hex() {
				errMsg = fmt.Sprintf("Incorrect snapshotter address extracted %s for specified slot %d: %s",
					snapshotterAddr.Hex(),
					details.submission.Request.SlotId,
					slotInfo.SnapshotterAddress.Hex())
			} else {
				if config.SettingsObj.VerifySubmissionDataSourceIndex {
					log.Debugf(
						"Verifying submission data source index for data market %s, slot ID %d, epoch ID %d project ID %s",
						details.dataMarketAddress,
						details.submission.Request.SlotId,
						details.submission.Request.EpochId,
						details.submission.Request.ProjectId,
					)
					// Extract the contract address from the projectID
					projectData := strings.Split(details.submission.Request.ProjectId, ":")

					// Ensure there are exactly three parts
					if len(projectData) != 3 {
						log.Printf("unexpected format for projectID: %s", details.submission.Request.ProjectId)
					}

					// Get the contract address from the project data
					extractedContractAddr := strings.ToLower(projectData[1])
					// for the data market address, get the data sources list
					dataSourcesList := config.SettingsObj.DataSourcesByMarket[strings.ToLower(details.dataMarketAddress)]
					if dataSourcesList == nil {
						log.Errorf("No data sources found for data market %s (lowercase: %s)", details.dataMarketAddress, strings.ToLower(details.dataMarketAddress))
						return fmt.Errorf("no data sources configured for data market %s", details.dataMarketAddress)
					}

					dataSourceIndex, err := fetchDataSourceIndex(
						details.dataMarketAddress,
						int64(details.submission.Request.EpochId),
						int64(details.submission.Request.SlotId),
						int64(len(dataSourcesList)),
						snapshotterAddr,
					)
					// Retrieve the contract address corresponding to the calculated pair contract index
					expectedContractAddr := strings.ToLower(dataSourcesList[dataSourceIndex])
					log.Debugf(
						"🔎 Fetched expected data source contract index for data market %s, slot ID %d, epoch ID %d: %d | Contract address at that index: %s",
						details.dataMarketAddress,
						details.submission.Request.SlotId,
						details.submission.Request.EpochId,
						dataSourceIndex,
						expectedContractAddr,
					)
					if err != nil {
						reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprint("Failed to fetch pair contract index: ", err.Error()), time.Now().String(), "High")
						log.Error("Failed to fetch pair contract index: ", err.Error())
					}

					if expectedContractAddr != extractedContractAddr {
						log.Errorf(
							"❌ Mismatched pair contract index for data market %s, epoch %d, slot %d project ID %s: provided %s by submission, expected %s from calculation",
							details.dataMarketAddress,
							details.submission.Request.EpochId,
							details.submission.Request.SlotId,
							details.submission.Request.ProjectId,
							extractedContractAddr,
							expectedContractAddr,
						)
						return errors.New("failed to verify pair contract index")
					} else {
						log.Debugf(
							"✅ Verified pair contract index for data market %s, epoch %d, slot %d project ID %s: extracted %s from submission, expected %s from calculation",
							details.dataMarketAddress,
							details.submission.Request.EpochId,
							details.submission.Request.SlotId,
							details.submission.Request.ProjectId,
							extractedContractAddr,
							expectedContractAddr,
						)
					}
				} else {
					log.Debugf(
						"🙅‍♀️ Skipping verification of submission data source index for data market %s , slot ID %d, epoch ID %d project ID %s",
						details.dataMarketAddress,
						details.submission.Request.SlotId,
						details.submission.Request.EpochId,
						details.submission.Request.ProjectId,
					)
				}
				currentEpochStr, _ := redis.Get(context.Background(), redis.CurrentEpoch(details.dataMarketAddress))
				log.Debugf("Current epoch for data market %s: %s", details.dataMarketAddress, currentEpochStr)
				if currentEpochStr == "" {
					errMsg = fmt.Sprintf("Current epochId not stored in redis for data market %s encountered while processing submission by snapshotter %s, epoch %d", details.dataMarketAddress, snapshotterAddr.Hex(), details.submission.Request.EpochId)
					reporting.SendFailureNotification("verifyAndStoreSubmission", errMsg, time.Now().String(), "High")
					log.Error(errMsg)
				} else {
					currentEpoch, err := strconv.Atoi(currentEpochStr)
					if err != nil {
						reporting.SendFailureNotification("verifyAndStoreSubmission", fmt.Sprintf("Cannot parse epoch %s stored in redis: %s", currentEpochStr, err.Error()), time.Now().String(), "High")
						log.Errorf("Cannot parse epoch %s stored in redis: %s", currentEpochStr, err.Error())
					} else if diff := uint64(currentEpoch) - details.submission.Request.EpochId; diff > 1 {
						errMsg = "Incorrect epochId supplied in request"
					}
				}
				// TODO: check for submission count exceeded in the current epoch for specific data market
				epochSubmissionExceededKey := redis.SlotEpochSubmissionCountExceeded(details.dataMarketAddress, strconv.FormatUint(details.submission.Request.SlotId, 10), details.submission.Request.EpochId)
				if val, _ := redis.Get(context.Background(), epochSubmissionExceededKey); val != "" {
					errMsg = "Slot epoch submission count exceeded for slot ID " + strconv.FormatUint(details.submission.Request.SlotId, 10)
				}
			}
			if errMsg != "" {
				log.Debugln("Snapshot submission rejected: ", errMsg)
				return errors.New("invalid snapshot")
			}
		}
	}
	key := redis.SubmissionKey(
		details.dataMarketAddress,
		details.submission.Request.EpochId,
		details.submission.Request.ProjectId,
		new(big.Int).SetUint64(details.submission.Request.SlotId).String(),
	)
	value := fmt.Sprintf("%s.%s", details.submissionId.String(), protojson.Format(details.submission))
	set := redis.SubmissionSetByHeaderKey(
		details.dataMarketAddress,
		details.submission.Request.EpochId,
		details.submission.Header,
	)

	if val, _ := redis.Get(context.Background(), key); val != "" {
		log.Debugln("Submission already exists: ", val)
		return nil
	}
	if err := redis.SetSubmission(context.Background(), key, value, set, 20*time.Minute); err != nil {
		log.Errorf("Error setting submission in Redis (slot ID: %s, epoch ID: %d, project ID: %s): %s", strconv.FormatUint(details.submission.Request.SlotId, 10), details.submission.Request.EpochId, details.submission.Request.ProjectId, err.Error())
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
	err = redis.RedisClient.Incr(context.Background(), redis.EpochSubmissionsCount(details.dataMarketAddress, details.submission.Request.EpochId)).Err()
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
	// this htable is the raw dump of all submissions for a given epoch and data market, expires after 30 minutes
	epochKey := redis.EpochSubmissionsKey(details.dataMarketAddress, details.submission.Request.EpochId)
	if err := redis.RedisClient.HSet(context.Background(), epochKey, details.submissionId.String(), submissionJSON).Err(); err != nil {
		log.Errorf("Failed to write submission details to Redis: %v", err)
		return fmt.Errorf("redis client failure: %s", err.Error())
	}
	if err := redis.RedisClient.Expire(context.Background(), epochKey, 30*time.Minute).Err(); err != nil {
		log.Errorf("Failed to set expiry for epoch submissions hash table %s: %v", epochKey, err)
		return fmt.Errorf("redis client failure: %s", err.Error())
	}
	if !isFullNode(snapshotterAddr.Hex()) {
		slotEpochCounterKey := redis.SlotEpochSubmissionsKey(details.dataMarketAddress, strconv.FormatUint(details.submission.Request.SlotId, 10), details.submission.Request.EpochId)
		count, err := redis.Incr(context.Background(), slotEpochCounterKey)
		// expire in 5 minutes
		if err := redis.RedisClient.Expire(context.Background(), slotEpochCounterKey, 5*time.Minute).Err(); err != nil {
			log.Errorf("Failed to set expiry for slot epoch counter %s: %v", slotEpochCounterKey, err)
			return fmt.Errorf("redis client failure: %s", err.Error())
		}
		if err != nil {
			log.Errorf("Failed to increment slot epoch counter: %v", err)
			return fmt.Errorf("redis client failure: %s", err.Error())
		} else {
			if count > 2 {
				errMsg := fmt.Sprintf("Slot epoch submission count exceeded for slot ID %s", strconv.FormatUint(details.submission.Request.SlotId, 10))
				log.Errorln("Slot epoch submission count exceeded: ", errMsg)
				redis.Set(
					context.Background(),
					redis.SlotEpochSubmissionCountExceeded(details.dataMarketAddress, strconv.FormatUint(details.submission.Request.SlotId, 10), details.submission.Request.EpochId),
					"true",
					5*time.Minute,
				)
				return errors.New(errMsg)
			}
		}
	}
	return nil
}

func getSnapshotterIntValue(snapshotAddr common.Address) *big.Int {
	// Convert the address into a lower case string
	snapshotterAddrString := strings.ToLower(snapshotAddr.Hex())

	// Convert the hexadecimal string to an integer (base 16)
	intVal := new(big.Int)
	intVal.SetString(snapshotterAddrString[2:], 16)
	return intVal
}

func fetchDataSourceIndex(dataMarketAddress string, epochID, slotID, size int64, snapshotterAddr common.Address) (int64, error) {
	// Calculate snapshotter hash
	snapshotterIntVal := getSnapshotterIntValue(snapshotterAddr)
	if snapshotterIntVal == nil {
		return 0, errors.New("failed to calculate snapshotter hash")
	}

	// Fetch current day
	currentDay, err := prost.FetchCurrentDay(dataMarketAddress)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch current day: %w", err)
	}
	if currentDay == nil {
		return 0, errors.New("currentDay is nil")
	}

	// Initialize a total variable for the calculation
	calculationSum := new(big.Int)

	// Perform the addition
	calculationSum.Add(big.NewInt(epochID), snapshotterIntVal)
	calculationSum.Add(calculationSum, big.NewInt(slotID))
	calculationSum.Add(calculationSum, currentDay)

	// Calculate contract index based on the size of initial pairs
	if size == 0 {
		return 0, errors.New("size parameter cannot be zero to avoid division by zero")
	}

	calculatedDataSourceIndex := new(big.Int).Mod(calculationSum, big.NewInt(size)).Int64()

	return calculatedDataSourceIndex, nil
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
		dataMarketAddressStr, ok := queueData["data_market_address"].(string)
		if !ok {
			log.Errorln("Invalid data_market_address format in queue data")
			continue
		}
		dataMarketAddress := common.HexToAddress(dataMarketAddressStr)
		log.Debugln("Data market address extracted from queue data: ", dataMarketAddress.Hex())
		// check if the data market address is in the set of data markets
		if !config.SettingsObj.IsValidDataMarketAddress(dataMarketAddress) {
			log.Errorln("Data market address not in the set of data markets")
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
			submissionId:      submissionId,
			submission:        &submission,
			dataMarketAddress: dataMarketAddressStr,
		}

		log.Debugln("Submission received for verification and storage with ID: ", submissionDetails.submissionId.String(), "and request: ", submissionDetails.submission.Request)
		err = s.verifyAndStoreSubmission(submissionDetails)
		if err != nil {
			log.Debugln("VerifyAndStore error:", err.Error())
		}
	}
}
