package redis

import (
	"context"
	"errors"
	"fmt"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs/reporting"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	log "github.com/sirupsen/logrus"
)

var RedisClient *redis.Client

type BoolCmd = redis.BoolCmd

// --- ADDED SCRIPT DEFINITION AND LOADING FUNCTION ---
const checkDuplicateAndIncrScript = `
-- KEYS[1]: submissionKey (e.g., submission:<epochID>:<projectID>:<slotID>:<dataMarketAddress>)
-- KEYS[2]: counterKey (e.g., submission:count:slot:<slotID>:<epochID>:<dataMarketAddress>)
-- KEYS[3]: exceededKey (e.g., submission:exceeded:slot:<slotID>:<epochID>:<dataMarketAddress>)
-- ARGV[1]: limit (e.g., "2")
-- ARGV[2]: counter expiry milliseconds (e.g., "300000" for 5 minutes)
-- ARGV[3]: exceeded flag expiry seconds (e.g., "300" for 5 minutes)
--
-- Returns:
-- 0: OK (Counter incremented)
-- 1: Duplicate submission
-- 2: Submission limit reached (Counter not incremented)

local submission_key = KEYS[1]
local counter_key = KEYS[2]
local exceeded_key = KEYS[3]
local limit = tonumber(ARGV[1])
local counter_expiry_ms = tonumber(ARGV[2])
local exceeded_expiry_s = tonumber(ARGV[3])

-- 1. Check for duplicate submission
if redis.call('EXISTS', submission_key) == 1 then
  return 1 -- Duplicate
end

-- 2. Check current count WITHOUT incrementing yet
local current_count_str = redis.call('GET', counter_key)
local current_count = 0
if current_count_str then
  current_count = tonumber(current_count_str) or 0
end

-- 3. Check if limit is already reached
if current_count >= limit then
  -- Set exceeded flag (and its expiry) unconditionally if limit reached
  redis.call('SET', exceeded_key, 'true', 'EX', exceeded_expiry_s)
  return 2 -- Limit Reached
end

-- 4. Limit is not reached, proceed to increment
local new_count = redis.call('INCR', counter_key)

-- 5. Set expiry on counter key unconditionally after incrementing
redis.call('PEXPIRE', counter_key, counter_expiry_ms)

-- 6. Return success indicator (counter was incremented)
return 0 -- OK
`

// CheckDuplicateAndIncrSha holds the SHA1 hash of the checkDuplicateAndIncrScript.
var CheckDuplicateAndIncrSha string

// LoadCheckDuplicateAndIncrScript loads the script into Redis and stores its SHA.
// It should be called once during application startup.
// It is idempotent and safe to call multiple times, though redundant.
func LoadCheckDuplicateAndIncrScript(ctx context.Context) (string, error) {
	if RedisClient == nil {
		return "", errors.New("redis client not initialized")
	}
	if CheckDuplicateAndIncrSha == "" {
		var err error
		CheckDuplicateAndIncrSha, err = RedisClient.ScriptLoad(ctx, checkDuplicateAndIncrScript).Result()
		if err != nil {
			log.Errorf("❌ Failed to load Check/Incr Redis Lua script: %v", err)
			return "", fmt.Errorf("failed to load check/incr redis script: %w", err)
		}
		log.Infof("🚀 Loaded Check/Incr Redis Lua script SHA: %s", CheckDuplicateAndIncrSha)
	}
	return CheckDuplicateAndIncrSha, nil
}

// TODO: Pool size failures to be checked
func NewRedisClient() *redis.Client {
	db, err := strconv.Atoi(config.SettingsObj.RedisDB)
	if err != nil {
		log.Fatalf("Incorrect redis db: %s", err.Error())
	}
	return redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%s", config.SettingsObj.RedisHost, config.SettingsObj.RedisPort), // Redis server address
		Password:     "",                                                                               // no password set
		DB:           db,
		PoolSize:     1000,
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
		DialTimeout:  5 * time.Second,
		IdleTimeout:  5 * time.Minute,
	})
}

func AddToSet(ctx context.Context, set string, keys ...string) error {
	if err := RedisClient.SAdd(ctx, set, keys).Err(); err != nil {
		reporting.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		return err
	}
	return nil
}

func GetSetKeys(ctx context.Context, set string) []string {
	return RedisClient.SMembers(ctx, set).Val()
}

func RemoveFromSet(ctx context.Context, set, key string) error {
	return RedisClient.SRem(context.Background(), set, key).Err()
}

func Delete(ctx context.Context, set string) error {
	return RedisClient.Del(ctx, set).Err()
}

func SetSubmission(ctx context.Context, key string, value string, set string, expiration time.Duration) error {
	if err := RedisClient.SAdd(ctx, set, key).Err(); err != nil {
		reporting.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		return err
	}

	if err := RedisClient.Expire(ctx, set, expiration).Err(); err != nil {
		reporting.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		return err
	}

	if err := RedisClient.Set(ctx, key, value, expiration).Err(); err != nil {
		reporting.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
		return err
	}

	return nil
}

func Get(ctx context.Context, key string) (string, error) {
	val, err := RedisClient.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil
		} else {
			reporting.SendFailureNotification("Redis error", err.Error(), time.Now().String(), "High")
			return "", err
		}
	}
	return val, nil
}

func Set(ctx context.Context, key, value string, expiration time.Duration) error {
	return RedisClient.Set(ctx, key, value, expiration).Err()
}

func Incr(ctx context.Context, key string) (int64, error) {
	result, err := RedisClient.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	return result, nil
}
