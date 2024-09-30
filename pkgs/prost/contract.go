package prost

import (
	"context"
	"crypto/tls"
	"math/big"
	"net/http"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs"
	"sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/redis"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	log "github.com/sirupsen/logrus"
)

var Instance *contract.Contract

var (
	Client *ethclient.Client
)

func ConfigureClient() {
	rpcClient, err := rpc.DialOptions(context.Background(), config.SettingsObj.ClientUrl, rpc.WithHTTPClient(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}))
	if err != nil {
		log.Fatal(err)
	}
	Client = ethclient.NewClient(rpcClient)
}
func ConfigureContractInstance() {
	Instance, _ = contract.NewContract(common.HexToAddress(config.SettingsObj.ContractAddress), Client)
}

func MustQuery[K any](ctx context.Context, call func() (val K, err error)) (K, error) {
	expBackOff := backoff.NewConstantBackOff(1 * time.Second)

	var val K
	operation := func() error {
		var err error
		val, err = call()
		return err
	}
	// Use the retry package to execute the operation with backoff
	err := backoff.Retry(operation, backoff.WithMaxRetries(expBackOff, 3))
	if err != nil {
		return *new(K), err
	}
	return val, err
}

func getExpirationTime() time.Time {
	daySize := 864000000 * time.Microsecond   // DAY_SIZE in microseconds converted to time.Duration
	expirationTime := time.Now().Add(daySize) // Calculate the expiration time by adding DAY_SIZE

	return expirationTime
}

// FetchCurrentDay fetches the current day from the contract and caches the result in Redis
func FetchCurrentDay() (*big.Int, error) {
	// Check Redis cache first
	cachedDay, err := redis.Get(context.Background(), pkgs.CurrentDay)
	if err == nil {
		// Cache hit: return the cached value
		currentDay := new(big.Int)
		currentDay.SetString(cachedDay, 10)
		return currentDay, nil
	}

	// Cache miss: fetch from contract: call the `currentDay` method
	currentDay, err := Instance.DayCounter(&bind.CallOpts{}, config.SettingsObj.DataMarketContractAddress)
	if err != nil {
		log.Printf("Failed to fetch current day from contract: %v", err)
		return nil, err
	}

	// Calculate expiration time
	expirationTime := getExpirationTime()

	// Set the current day in Redis with the calculated expiration duration
	if err := redis.Set(context.Background(), pkgs.CurrentDay, currentDay.String(), time.Until(expirationTime)); err != nil {
		log.Printf("Failed to cache current day in Redis: %v", err)
		return nil, err
	}

	return currentDay, nil
}
