package prost

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/big"
	"net/http"
	"sequencer-dequeuer/config"
	protocolStateContractABIGen "sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/redis"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	log "github.com/sirupsen/logrus"
)

var (
	Client   *ethclient.Client
	Instance *protocolStateContractABIGen.Contract
)

func ConfigureClient() {
	rpcClient, err := rpc.DialOptions(context.Background(), config.SettingsObj.ClientUrl, rpc.WithHTTPClient(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}))
	if err != nil {
		log.Fatal(err)
	}
	Client = ethclient.NewClient(rpcClient)
}

func ConfigureContractInstance() {
	Instance, _ = protocolStateContractABIGen.NewContract(common.HexToAddress(config.SettingsObj.ContractAddress), Client)
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

func getExpirationTime(epochID, daySize int64, epochsInADay int64) time.Time {
	// DAY_SIZE in microseconds
	updatedDaySize := time.Duration(daySize) * time.Microsecond

	// Calculate the duration of each epoch
	epochDuration := updatedDaySize / time.Duration(epochsInADay)

	// Calculate the number of epochs left for the day
	remainingEpochs := epochID % int64(epochsInADay)

	// Calculate the expiration duration
	expirationDuration := epochDuration * time.Duration(remainingEpochs)

	// Set a buffer of 10 seconds to expire slightly earlier
	bufferDuration := 10 * time.Second

	// Calculate the expiration time by subtracting the buffer duration
	expirationTime := time.Now().Add(expirationDuration - bufferDuration)

	return expirationTime
}

// FetchCurrentDay fetches the current day from the contract and caches the result in Redis
func FetchCurrentDay(dataMarketAddress string) (*big.Int, error) {
	// Check Redis cache
	cachedDay, err := redis.Get(context.Background(), redis.DataMarketCurrentDay(dataMarketAddress))
	if err != nil {
		return nil, err
	}

	// Cache hit: return the cached value
	currentDay := new(big.Int)
	currentDay.SetString(cachedDay, 10)

	// Return error if current day is 0
	if currentDay.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("current day is 0")
	}

	return currentDay, nil
}
