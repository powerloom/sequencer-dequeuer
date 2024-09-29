package dequeuer

import (
	"context"
	"sequencer-dequeuer/pkgs"
	"testing"

	"github.com/alicebob/miniredis/v2"
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
