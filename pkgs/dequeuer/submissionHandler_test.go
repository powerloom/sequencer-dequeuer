package dequeuer

import (
	"context"
	"fmt"
	"math/big"
	"sequencer-dequeuer/pkgs"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
)

func TestVerifyFlaggedSnapshotter(t *testing.T) {
	// Create a mini Redis server for testing
	mockRedis, err := miniredis.Run()
	assert.NoError(t, err)
	defer mockRedis.Close()

	// Initialize Redis client with mock server
	redisClient := redis.NewClient(&redis.Options{
		Addr: mockRedis.Addr(),
	})

	// Test snapshotter address
	snapshotterAddr := "0x123456789abcdef"

	// Set up the flagged snapshotter key in Redis (mock data)
	flaggedSnapshotterKey := pkgs.FlaggedSnapshotterKey

	// Add the flagged snapshotter to Redis
	_, err = mockRedis.SAdd(flaggedSnapshotterKey, snapshotterAddr)
	assert.NoError(t, err)

	tests := []struct {
		name              string
		snapshotterAddr   string
		expectedIsFlagged bool
		expectedError     bool
	}{
		{
			name:              "Snapshotter is not flagged",
			snapshotterAddr:   "0xabcdef123456789",
			expectedIsFlagged: false,
			expectedError:     false,
		},
		{
			name:              "Snapshotter is flagged",
			snapshotterAddr:   snapshotterAddr,
			expectedIsFlagged: true,
			expectedError:     false,
		},
		{
			name:              "Redis query error",
			snapshotterAddr:   snapshotterAddr,
			expectedIsFlagged: false,
			expectedError:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.expectedError {
				mockRedis.Close() // Simulate Redis query error
			}

			isFlagged, err := redisClient.SIsMember(context.Background(), flaggedSnapshotterKey, test.snapshotterAddr).Result()
			if test.expectedError {
				assert.Error(t, err, "Expected Redis query error")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, test.expectedIsFlagged, isFlagged)
			}
		})
	}
}

func TestPairContractIndexCalculation(t *testing.T) {
	// Inputs to the function
	epochID := int64(5) // Test value for epochID
	slotID := int64(3)  // Test value for slotID
	size := int64(10)   // Test size of the initial pairs
	snapshotterAddr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// Mocked return values for the function inputs
	snapshotterHash := getSnapshotterHash(snapshotterAddr) // Assume this hash is calculated correctly
	fmt.Println("Snapshotter hash is: ", snapshotterHash)

	currentDay := big.NewInt(40) // Mocked value for current day

	// Step-by-step manual calculation for expected result
	calculationSum := new(big.Int)
	calculationSum.Add(big.NewInt(epochID), snapshotterHash) // Add epochID and snapshotterHash
	calculationSum.Add(calculationSum, big.NewInt(slotID))   // Add slotID to the result
	calculationSum.Add(calculationSum, currentDay)           // Add currentDay to the result

	// Calculate contract index based on the size of initial pairs
	pairContractIndex := new(big.Int).Mod(calculationSum, big.NewInt(size)).Int64()

	// Print the calculated pair contract index
	fmt.Println("Pair contract index is", pairContractIndex)
}
