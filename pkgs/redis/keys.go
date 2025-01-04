package redis

import (
	"fmt"
	"math/big"
	"sequencer-dequeuer/pkgs"
	"strings"
)

// TODO: submission keys should be separated by data market address

func CurrentEpoch(dataMarketAddress string) string {
	return fmt.Sprintf("%s.%s", strings.ToLower(dataMarketAddress), pkgs.CurrentEpoch)
}

func FlaggedSnapshotterKey(dataMarketAddress string) string {
	return fmt.Sprintf("%s.%s", strings.ToLower(dataMarketAddress), pkgs.FlaggedSnapshotterKey)
}

func SubmissionSetByHeaderKey(dataMarketAddress string, epoch uint64, header string) string {
	return fmt.Sprintf("%s.%s.%d.%s", pkgs.CollectorKey, strings.ToLower(dataMarketAddress), epoch, header)
}

func SubmissionKey(dataMarketAddress string, epochId uint64, projectId, slotId string) string {
	return fmt.Sprintf("%s.%d.%s.%s", strings.ToLower(dataMarketAddress), epochId, projectId, slotId)
}

func ContractStateVariable(varName string) string {
	return fmt.Sprintf("ProtocolState.%s", varName)
}

func SlotEpochSubmissionCountExceeded(dataMarketAddress string, slotId string, epochId uint64) string {
	return fmt.Sprintf("SlotEpochSubmissionCountExceeded.%s.%s.%d", strings.ToLower(dataMarketAddress), slotId, epochId)
}

func SlotInfo(slotId string) string {
	return fmt.Sprintf("%s.%s", ContractStateVariable("SlotInfo"), slotId)
}

func EpochSubmissionsCount(dataMarketAddress string, epochId uint64) string {
	return fmt.Sprintf("%s.%s.%d", pkgs.EpochSubmissionsCountKey, strings.ToLower(dataMarketAddress), epochId)
}

func EpochSubmissionsKey(dataMarketAddress string, epochId uint64) string {
	return fmt.Sprintf("%s.%s.%d", pkgs.EpochSubmissionsKey, strings.ToLower(dataMarketAddress), epochId)
}

func SlotEpochSubmissionsKey(dataMarketAddress string, slotId string, epochId uint64) string {
	return fmt.Sprintf("SlotEpochCounter.%s.%s.%d", strings.ToLower(dataMarketAddress), slotId, epochId)
}

func LastSimulatedSubmission(dataMarketAddress string) string {
	return fmt.Sprintf("%s.%s", pkgs.LastSimulatedSubmissionKey, strings.ToLower(dataMarketAddress))
}

func LastSnapshotSubmission(dataMarketAddress string) string {
	return fmt.Sprintf("%s.%s", pkgs.LastSnapshotSubmissionKey, strings.ToLower(dataMarketAddress))
}

func GetSnapshotterSlotSubmissionsHtable(dataMarketAddress string, snapshotterAddress string, slotID *big.Int) string {
	return fmt.Sprintf("snapshotter:%s:%s:%d:slot_submissions", strings.ToLower(dataMarketAddress), strings.ToLower(snapshotterAddress), slotID)
}

func GetSnapshotterSubmissionCountInSlot(dataMarketAddress string, snapshotterAddress string, slotID *big.Int) string {
	return fmt.Sprintf("snapshotter:%s:%s:%d:submission_count", strings.ToLower(dataMarketAddress), strings.ToLower(snapshotterAddress), slotID)
}

func GetSnapshotterNodeVersion(dataMarketAddress string, snapshotterAddress string, slotID *big.Int) string {
	return fmt.Sprintf("snapshotter:%s:%s:%d:node_version", strings.ToLower(dataMarketAddress), strings.ToLower(snapshotterAddress), slotID)
}

func DataMarketCurrentDay(dataMarketAddress string) string {
	return fmt.Sprintf("%s.%s", strings.ToLower(dataMarketAddress), pkgs.CurrentDay)
}
