package dequeuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs"
	"sequencer-dequeuer/pkgs/prost"
	"sequencer-dequeuer/pkgs/redis"
	"sequencer-dequeuer/pkgs/reporting"
	"sequencer-dequeuer/pkgs/utils"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
)

var SubmissionHandlerInstance *SubmissionHandler

// SubmissionDetails encapsulates the data needed for processing a submission
type SubmissionDetails struct {
	submissionID      uuid.UUID
	submission        *pkgs.SnapshotSubmission
	dataMarketAddress string
}

type SubmissionHandler struct {
	// Keep track of epochs for which we've already set expiry
	expirySetActiveSnapshottersForEpoch     map[string]bool
	expirySetActiveSnapshottersForEpochLock sync.RWMutex
}

type SnapshotData struct {
	EpochID     uint64
	SlotID      uint64
	Deadline    uint64
	SnapshotCID string
	ProjectID   string
	Timestamp   int64
}

type SlotInfo struct {
	SnapshotterAddress common.Address // address
	NodePrice          *big.Int       // uint256
	AmountSentOnL1     *big.Int       // uint256
	MintedOn           *big.Int       // uint256
	BurnedOn           *big.Int       // uint256
	LastUpdated        *big.Int       // uint256
	IsLegacy           bool           // bool
	ClaimedTokens      bool           // bool
	Active             bool           // bool
	IsKyced            bool           // bool
}

func isFullNode(addr string) bool {
	for _, node := range config.SettingsObj.FullNodes {
		if node == addr {
			return true
		}
	}
	return false
}

func (sh *SubmissionHandler) Start() {
	// Initialize the expiry tracking map
	sh.expirySetActiveSnapshottersForEpoch = make(map[string]bool)

	// Start a cleanup goroutine
	go sh.periodicActiveExpiryReset()

	// Load Lua script at startup (using the function from the redis package)
	_, err := redis.LoadCheckDuplicateAndIncrScript(context.Background())
	if err != nil {
		log.Fatalf("Cannot start SubmissionHandler: Lua script loading failed: %v", err)
	}

	// Implement the submission handling logic here
	sh.startSubmissionDequeuer()
}

// verifyAndStoreSubmission verifies the submission and stores it in Redis
func (s *SubmissionHandler) verifyAndStoreSubmission(details SubmissionDetails) error {
	// Recover the snapshotter address from the signature
	snapshotterAddr, err := utils.RecoverAddress(utils.HashRequest(details.submission.Request), common.Hex2Bytes(details.submission.Signature))
	if err != nil {
		log.Errorf("Failed to recover snapshotter address: %s", err.Error())
		return fmt.Errorf("snapshotter address recovery error: %s", err.Error())
	}

	// Log and store node version if present, otherwise set default version
	var nodeVersion string
	if details.submission.NodeVersion != nil && *details.submission.NodeVersion != "" {
		nodeVersion = *details.submission.NodeVersion
	} else {
		nodeVersion = "v0.0.0"
	}

	log.Infof("📦 Node version %s detected for slot %d, project %s, epoch %d, data market %s",
		nodeVersion,
		details.submission.Request.SlotId,
		details.submission.Request.ProjectId,
		details.submission.Request.EpochId,
		details.dataMarketAddress)

	// Store node version in Redis
	nodeVersionKey := redis.GetSnapshotterNodeVersion(
		details.dataMarketAddress,
		new(big.Int).SetUint64(details.submission.Request.SlotId))
	if err := redis.Set(context.Background(), nodeVersionKey, nodeVersion, 0); err != nil {
		log.Errorf("Failed to store node version in Redis: %v", err)
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

	// check for slot ID to extracted snapshotter address from protocol state cacher
	var slotInfo SlotInfo
	slotInfoStr, err := redis.Get(context.Background(), redis.SlotInfo(strconv.FormatUint(details.submission.Request.SlotId, 10)))
	if err != nil {
		errMsg := fmt.Sprintf("Failed to get slotInfo: %s", err)
		reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
		log.Error(errMsg)
		return fmt.Errorf("failed to get slotInfo: %s", err)
	}
	// unmarshal slotInfoStr to slotInfo
	if err := json.Unmarshal([]byte(slotInfoStr), &slotInfo); err != nil {
		errMsg := fmt.Sprintf("Failed to unmarshal slotInfo: %s", err.Error())
		reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
		log.Error(errMsg)
		return fmt.Errorf("failed to unmarshal slotInfo: %s", err.Error())
	}

	if snapshotterAddr.Hex() != slotInfo.SnapshotterAddress.Hex() {
		errMsg := fmt.Sprintf("Incorrect snapshotter address extracted %s for specified slot %d from slotInfo: %s", snapshotterAddr.Hex(), details.submission.Request.SlotId, slotInfo.SnapshotterAddress.Hex())
		reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
		log.Error(errMsg)
		return fmt.Errorf("incorrect snapshotter address extracted %s for specified slot %d from slotInfo: %s", snapshotterAddr.Hex(), details.submission.Request.SlotId, slotInfo.SnapshotterAddress.Hex())
	}

	// Check if epochID field exists in the raw JSON
	rawJSON, err := json.Marshal(details.submission.Request)
	if err != nil {
		log.Errorf("Failed to marshal request: %v", err)
		return err
	}

	var rawRequest map[string]interface{}
	if err := json.Unmarshal(rawJSON, &rawRequest); err != nil {
		log.Errorf("Failed to unmarshal request to map: %v", err)
		return err
	}
	// Check if epochID field exists and is explicitly set to 0 for simulation submissions
	_, epochIDExists := rawRequest["epochId"]
	if !epochIDExists || details.submission.Request.EpochId == 0 {
		log.Infof("Received simulated submission from slot %d for data market %s: %s", details.submission.Request.SlotId, details.dataMarketAddress, details.submission.String())
		// Key to track the last simulation submission
		simulationKey := redis.LastSimulatedSubmission(details.dataMarketAddress, details.submission.Request.SlotId)
		if err := redis.RedisClient.Set(context.Background(), simulationKey, time.Now().Unix(), 0).Err(); err != nil {
			errMsg := fmt.Sprintf("Failed to set last simulated submission timestamp for slot %d, data market %s in Redis: %s", details.submission.Request.SlotId, details.dataMarketAddress, err.Error())
			reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
			log.Error(errMsg)
			return fmt.Errorf("redis client failure: %s", err.Error())
		} else {
			log.Infof("🫣 Successfully set last simulated submission timestamp for slot %d, data market %s in Redis: %s", details.submission.Request.SlotId, details.dataMarketAddress, simulationKey)
		}

		return nil
	}

	// NOTE: removing the setting of GetSnapshotterSubmissionCountInSlot because it is not used anywhere in the sequencer components

	// NOTE: removing the constructor of SnapshotData because it is no longer needed for the setting of GetSnapshotterSlotSubmissionsHtable

	var counterIncremented bool = false

	if !isFullNode(snapshotterAddr.Hex()) {

		// --- Lua Script Execution for Duplicate/Count Check ---
		slotIDStr := strconv.FormatUint(details.submission.Request.SlotId, 10)
		counterKey := redis.SlotEpochSubmissionsKey(details.dataMarketAddress, slotIDStr, details.submission.Request.EpochId)
		limit := "2" // TODO: Make configurable?
		counterExpiryMs := strconv.FormatInt(5*time.Minute.Milliseconds(), 10)

		// Update scriptArgs and scriptKeys
		scriptArgs := []interface{}{limit, counterExpiryMs}
		scriptKeys := []string{counterKey} // Only counterKey needed now

		log.Debugf("Executing CheckDuplicateAndIncrScript for non-full node. Key: %s, Args: %v", counterKey, scriptArgs)
		resultCmd := redis.RedisClient.EvalSha(context.Background(), redis.CheckDuplicateAndIncrSha, scriptKeys, scriptArgs...)
		scriptResult, err := resultCmd.Int64()

		if err != nil {
			if strings.Contains(err.Error(), "NOSCRIPT") {
				log.Warnf("NOSCRIPT error executing CheckDuplicateAndIncrScript, attempting to reload and retry.")
				newSha, loadErr := redis.LoadCheckDuplicateAndIncrScript(context.Background())
				if loadErr != nil {
					errMsg := fmt.Sprintf("Failed to reload CheckDuplicateAndIncrScript after NOSCRIPT error: %v", loadErr)
					reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
					log.Error(errMsg)
					return errors.New(errMsg)
				}
				resultCmd = redis.RedisClient.EvalSha(context.Background(), newSha, scriptKeys, scriptArgs...)
				scriptResult, err = resultCmd.Int64()
			}
		}

		if err != nil {
			errMsg := fmt.Sprintf("Error executing CheckDuplicateAndIncrScript: %v", err)
			reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
			log.Error(errMsg)
			return errors.New(errMsg)
		}

		switch scriptResult {
		case 0: // OK - Counter was incremented
			log.Debugf("Lua script OK for slot %d, epoch %d. Counter incremented.", details.submission.Request.SlotId, details.submission.Request.EpochId)
			counterIncremented = true // Set the flag
		case 1: // Limit Reached
			errMsg := fmt.Sprintf("Lua script reported slot epoch submission count exceeded for slotID %d, epoch %d", details.submission.Request.SlotId, details.submission.Request.EpochId)
			reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
			log.Error(errMsg)
			return errors.New(errMsg)
		default:
			errMsg := fmt.Sprintf("Unexpected result from CheckDuplicateAndIncrScript: %d", scriptResult)
			reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
			log.Error(errMsg)
			return errors.New(errMsg)
		}

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

			// Retrieve the list of data sources associated with the specified data market address
			dataSourcesList := config.SettingsObj.DataSourcesByMarket[strings.ToLower(details.dataMarketAddress)]
			if dataSourcesList == nil {
				errMsg := fmt.Sprintf("Data source configuration missing for data market: %s (lowercase: %s)", details.dataMarketAddress, strings.ToLower(details.dataMarketAddress))
				reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
				log.Error(errMsg)

				if counterIncremented {
					log.Warnf("Data source list missing after Lua script incremented counter. Attempting rollback (decrement) for slot %d, epoch %d.", details.submission.Request.SlotId, details.submission.Request.EpochId)
					attemptCounterRollback(counterKey, details.submission.Request.SlotId, details.submission.Request.EpochId)
				}
				return fmt.Errorf("no data sources found for the specified data market: %s", details.dataMarketAddress) // Return original error
			}

			// Fetch the data source index for the submission
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
				errMsg := fmt.Sprintf("Failed to fetch pair contract index: %s", err.Error())
				reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, errMsg, time.Now().String(), "High")
				log.Error(errMsg)

				if counterIncremented {
					log.Warnf("Data source index fetch failed after Lua script incremented counter. Attempting rollback (decrement) for slot %d, epoch %d.", details.submission.Request.SlotId, details.submission.Request.EpochId)
					attemptCounterRollback(counterKey, details.submission.Request.SlotId, details.submission.Request.EpochId)
				}
				return errors.New("failed to verify pair contract index") // Return original error
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

				if counterIncremented {
					log.Warnf("Data source index mismatch after Lua script incremented counter. Attempting rollback (decrement) for slot %d, epoch %d.", details.submission.Request.SlotId, details.submission.Request.EpochId)
					attemptCounterRollback(counterKey, details.submission.Request.SlotId, details.submission.Request.EpochId)
				}
				return errors.New("failed to verify pair contract index") // Return original error
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

		// NOTE: removing the check of submitted epochId against arbitrary EpochAcceptanceWindow

		// NOTE: removing the saving of GetSnapshotterSlotSubmissionsHtable to Redis because it is not used anywhere in the sequencer components

		// NOTE: removing the setting of lastPingKey because it is not used anywhere in the sequencer components of snapshotter dashboard

		snapshotKey := redis.LastSnapshotSubmission(details.dataMarketAddress, details.submission.Request.SlotId)
		if err := redis.RedisClient.Set(context.Background(), snapshotKey, time.Now().Unix(), 0).Err(); err != nil {
			logMsg := fmt.Sprintf("Failed to set last snapshot submission timestamp for slot %d, data market address %s in Redis: %v", details.submission.Request.SlotId, details.dataMarketAddress, err)
			reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, logMsg, time.Now().String(), "High")
			log.Error(logMsg)
			return fmt.Errorf("redis client failure: %s", err.Error())
		} else {
			log.Infof("🫣 Successfully set last snapshot submission timestamp for slot %d, data market %s in Redis: %s", details.submission.Request.SlotId, details.dataMarketAddress, snapshotKey)
		}
	}

	submissionKey := redis.SubmissionKey(
		details.dataMarketAddress,
		details.submission.Request.EpochId,
		details.submission.Request.ProjectId,
		new(big.Int).SetUint64(details.submission.Request.SlotId).String(),
	)

	value := fmt.Sprintf("%s.%s", details.submissionID.String(), protojson.Format(details.submission))

	// Create the submission set key
	submissionSetByHeaderKey := redis.SubmissionSetByHeaderKey(
		details.dataMarketAddress,
		details.submission.Request.EpochId,
		details.submission.Header,
	)

	// Store the submission in Redis
	if err := redis.SetSubmission(context.Background(), submissionKey, value, submissionSetByHeaderKey, 20*time.Minute); err != nil {
		logMsg := fmt.Sprintf("Failed to set submission (slot ID: %d, epoch ID: %d, project ID: %s) in Redis: %s",
			details.submission.Request.SlotId, details.submission.Request.EpochId, details.submission.Request.ProjectId, err.Error())
		reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, logMsg, time.Now().String(), "High")
		log.Error(logMsg)
		return err
	}

	log.Debugf(
		"✅ Successfully set submission with set %s and key %s for slot %d, epoch %d, project %s",
		submissionSetByHeaderKey,
		submissionKey,
		details.submission.Request.SlotId,
		details.submission.Request.EpochId,
		details.submission.Request.ProjectId,
	)

	// Add slot to a set of active slots for this epoch
	activeSnapshottersKey := redis.ActiveSnapshottersForEpoch(details.dataMarketAddress, details.submission.Request.EpochId)
	if err := redis.RedisClient.SAdd(context.Background(), activeSnapshottersKey, strconv.FormatUint(details.submission.Request.SlotId, 10)).Err(); err != nil {
		logMsg := fmt.Sprintf("Error tracking active slot: %s", err.Error())
		reporting.SendFailureNotification(pkgs.VerifyAndStoreSubmission, logMsg, time.Now().String(), "High")
		log.Error(logMsg)
	}

	// sets the expiry status in-memory as well so we dont make round trips to redis for this
	if !s.isActiveSnapshotterExpirySetForEpoch(details.dataMarketAddress, details.submission.Request.EpochId) {
		if err := redis.RedisClient.Expire(context.Background(), activeSnapshottersKey, 30*time.Minute).Err(); err != nil {
			log.Errorf("Failed to set expiry for active snapshotters set: %v", err)
		} else {
			// Mark that we've set expiry for this epoch
			s.markActiveSnapshotterExpirySetForEpoch(details.dataMarketAddress, details.submission.Request.EpochId)
			log.Debugf("Set expiry for active snapshotters for epoch %d", details.submission.Request.EpochId)
		}
	}

	// Marshal the submission data to store in Redis
	submissionJSON, err := json.Marshal(details.submission)
	if err != nil {
		log.Errorf("Error serializing submission: %v", err)
		return fmt.Errorf("json marshalling error: %s", err.Error())
	}

	// This Htable is the raw dump of all submissions for a given epoch and data market
	epochSubmissionKey := redis.EpochSubmissionsKey(details.dataMarketAddress, details.submission.Request.EpochId)
	if err := redis.RedisClient.HSet(context.Background(), epochSubmissionKey, details.submissionID.String(), submissionJSON).Err(); err != nil {
		log.Errorf("Failed to write submission details to Redis: %v", err)
		return fmt.Errorf("redis client failure: %s", err.Error())
	}

	// Set the expiry for the epoch submissions hash table
	if err := redis.RedisClient.Expire(context.Background(), epochSubmissionKey, 30*time.Minute).Err(); err != nil {
		log.Errorf("Failed to set expiry for epoch submissions hash table %s: %v", epochSubmissionKey, err)
		return fmt.Errorf("redis client failure: %s", err.Error())
	}

	return nil
}

func attemptCounterRollback(counterKey string, slotID uint64, epochID uint64) {
	log.Warnf("Attempting counter rollback (decrement) for key '%s' (slot %d, epoch %d).", counterKey, slotID, epochID)
	if decrErr := redis.RedisClient.Decr(context.Background(), counterKey).Err(); decrErr != nil {
		log.Errorf("Failed to decrement counter key '%s' during rollback: %v", counterKey, decrErr)
	} else {
		log.Infof("↩️ Successfully decremented counter key '%s' during rollback.", counterKey)
	}
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
	log.Debugln("🚀 Submission Dequeuer started...")

	for {
		// Fetch data from Redis queue
		result, err := redis.RedisClient.BRPop(context.Background(), 0, "submissionQueue").Result()
		if err != nil {
			log.Errorf("Error fetching from Redis queue: %v", err)
			continue
		}

		if len(result) < 2 {
			log.Errorln("Invalid data received from Redis queue, skipping this entry")
			continue
		}

		var queueData map[string]interface{}
		if err := json.Unmarshal([]byte(result[1]), &queueData); err != nil {
			log.Errorf("Failed to parse queue data: %v", err)
			continue
		}

		dataMarketAddressStr, ok := queueData["data_market_address"].(string)
		if !ok {
			log.Errorln("Missing or invalid 'data_market_address' in queue data")
			continue
		}
		dataMarketAddress := common.HexToAddress(dataMarketAddressStr)
		// Check if the data market address is in the set of data markets
		if !config.SettingsObj.IsValidDataMarketAddress(dataMarketAddress) {
			log.Errorln("Data market address not found in the set of data markets")
			continue
		}

		// REMOVED: Increment all-time total of incoming submission count for this data market. This metric is useless.

		submissionIDStr, ok := queueData["submission_id"].(string)
		if !ok {
			log.Errorln("Missing or invalid 'submission_id' in queue data")
			continue
		}

		submissionID := uuid.New()
		err = submissionID.UnmarshalText([]byte(submissionIDStr))
		if err != nil {
			log.Errorf("Failed to parse 'submission_id': %v", err)
			continue
		}

		submissionStr, ok := queueData["data"].(string)
		if !ok {
			log.Errorln("Missing or invalid 'data' field in queue data")
			continue
		}

		var submission pkgs.SnapshotSubmission
		err = json.Unmarshal([]byte(submissionStr), &submission)
		if err != nil {
			log.Errorf("Failed to parse submission data for ID '%s': %v", submissionID.String(), err)
			continue
		}

		submissionDetails := SubmissionDetails{
			submissionID:      submissionID,
			submission:        &submission,
			dataMarketAddress: dataMarketAddressStr,
		}

		log.Infof("🔄 Processing submission with ID %s for data market address %s with request details %+v", submissionDetails.submissionID.String(), submissionDetails.dataMarketAddress, submissionDetails.submission.Request)

		if err := s.verifyAndStoreSubmission(submissionDetails); err != nil {
			log.Errorf("❌ Failed to verify and store submission with ID '%s': %s", submissionDetails.submissionID.String(), err.Error())
		} else {
			// Log success only if verifyAndStoreSubmission returned nil error
			log.Infof("✅ Successfully verified and stored submission with ID: %s", submissionDetails.submissionID.String())
		}
	}
}

// isActiveSnapshotterExpirySetForEpoch checks if expiry has already been set for this data market and epoch
func (s *SubmissionHandler) isActiveSnapshotterExpirySetForEpoch(dataMarketAddress string, epochID uint64) bool {
	key := fmt.Sprintf("%s:%d", dataMarketAddress, epochID)

	s.expirySetActiveSnapshottersForEpochLock.RLock()
	isSet := s.expirySetActiveSnapshottersForEpoch[key]
	s.expirySetActiveSnapshottersForEpochLock.RUnlock()

	return isSet
}

// markActiveSnapshotterExpirySetForEpoch marks that expiry has been set for this data market and epoch
func (s *SubmissionHandler) markActiveSnapshotterExpirySetForEpoch(dataMarketAddress string, epochID uint64) {
	key := fmt.Sprintf("%s:%d", dataMarketAddress, epochID)

	s.expirySetActiveSnapshottersForEpochLock.Lock()
	s.expirySetActiveSnapshottersForEpoch[key] = true
	s.expirySetActiveSnapshottersForEpochLock.Unlock()
}

// periodicActiveExpiryReset recreates the tracking map at regular intervals
func (s *SubmissionHandler) periodicActiveExpiryReset() {
	// Reset every 30 minutes to avoid memory leaks
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.expirySetActiveSnapshottersForEpochLock.Lock()
		// Log the reset with the map size
		mapSize := len(s.expirySetActiveSnapshottersForEpoch)
		log.Infof("Resetting expirySetActiveSnapshottersForEpoch map (size was %d entries)", mapSize)

		// Create a new map
		s.expirySetActiveSnapshottersForEpoch = make(map[string]bool)
		s.expirySetActiveSnapshottersForEpochLock.Unlock()
	}
}
