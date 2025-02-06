package dequeuer

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sequencer-dequeuer/pkgs"
	protocolStateABIGen "sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/redis"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

func CleanupSubmissionSet(ctx context.Context, dataMarketAddr string) error {
	log.Debugln("Cleaning up old submission set by header key for", dataMarketAddr)
	// Get current epoch
	currentEpochStr, err := redis.Get(ctx, redis.CurrentEpoch(dataMarketAddr))
	if err != nil {
		log.Errorf("Failed to get current epoch for cleanup: %v", err)
		return err
	}

	currentEpoch, err := strconv.ParseUint(currentEpochStr, 10, 64)
	if err != nil {
		log.Errorf("Failed to parse current epoch %s: %v", currentEpochStr, err)
		return err
	}

	// Use SCAN instead of KEYS
	var cursor uint64
	var keys []string
	pattern := fmt.Sprintf("%s.%s.*.*", pkgs.CollectorKey, strings.ToLower(dataMarketAddr))

	for {
		var batch []string
		var err error
		batch, cursor, err = redis.RedisClient.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			log.Errorf("Failed to scan submission set keys: %v", err)
			return err
		}
		// extract epoch ID from key
		for _, key := range batch {
			epochID := strings.Split(key, ".")[2]
			epochIDInt, err := strconv.ParseUint(epochID, 10, 64)
			if err != nil {
				log.Errorf("Failed to parse epoch ID %s: %v", epochID, err)
				continue
			}
			if epochIDInt < currentEpoch-10 {
				keys = append(keys, key)
			}
		}
		keys = append(keys, batch...)

		// If cursor is 0, we've completed the scan
		if cursor == 0 {
			break
		}
	}

	// Delete the keys in batch
	if len(keys) > 0 {
		if err := redis.RedisClient.Del(ctx, keys...).Err(); err != nil {
			log.Errorf("Failed to delete keys in batch: %v", err)
		} else {
			log.Debugf("Successfully deleted %d submission set header keys for data market %s", len(keys), dataMarketAddr)
		}
	}
	return nil
}

func cleanupOldSubmissions(ctx context.Context, dataMarketAddr string) {
	// Get current epoch
	currentEpochStr, err := redis.Get(ctx, redis.CurrentEpoch(dataMarketAddr))
	if err != nil {
		log.Errorf("Failed to get current epoch for cleanup: %v", err)
		return
	}

	currentEpoch, err := strconv.ParseUint(currentEpochStr, 10, 64)
	if err != nil {
		log.Errorf("Failed to parse current epoch %s: %v", currentEpochStr, err)
		return
	}

	// Don't cleanup if we're in early epochs
	if currentEpoch <= 10 {
		return
	}

	targetEpoch := currentEpoch - 10

	// For each slot
	for slotID := uint64(1); slotID <= 6000; slotID++ {
		// Get slot info to get snapshotter address
		var slotInfo protocolStateABIGen.PowerloomDataMarketSlotInfo
		slotInfoStr, err := redis.Get(ctx, redis.SlotInfo(strconv.FormatUint(slotID, 10)))
		if err != nil || slotInfoStr == "" {
			continue
		}

		if err := json.Unmarshal([]byte(slotInfoStr), &slotInfo); err != nil {
			continue
		}

		// Get the hash table key for this snapshotter and slot
		htableKey := redis.GetSnapshotterSlotSubmissionsHtable(
			dataMarketAddr,
			slotInfo.SnapshotterAddress.Hex(),
			new(big.Int).SetUint64(slotID),
		)

		// Delete all entries from epoch 1 to targetEpoch
		for epochID := uint64(1); epochID <= targetEpoch; epochID++ {
			if err := redis.RedisClient.HDel(ctx, htableKey, strconv.FormatUint(epochID, 10)).Err(); err != nil {
				if err.Error() != "redis: nil" {
					log.Errorf("Failed to delete epoch %d from hash %s: %v", epochID, htableKey, err)
				}
			} else {
				log.Debugf("Deleted epoch %d from hash %s", epochID, htableKey)
			}
		}
	}
}
