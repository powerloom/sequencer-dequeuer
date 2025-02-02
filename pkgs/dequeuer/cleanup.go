package dequeuer

import (
	"context"
	"encoding/json"
	"math/big"
	protocolStateABIGen "sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/redis"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
)

func StartCleanupRoutine(ctx context.Context, dataMarketAddresses []string) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, dataMarketAddr := range dataMarketAddresses {
					cleanupOldSubmissions(ctx, dataMarketAddr)
				}
			}
		}
	}()
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
