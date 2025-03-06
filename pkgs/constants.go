package pkgs

import "time"

// Process Name Constants
// process : identifier
const (
	VerifyAndStoreSubmission = "SequencerDequeuer: VerifyAndStoreSubmission"
)

// General Key Constants
const (
	CurrentDay                      = "CurrentDayKey"
	CurrentEpoch                    = "CurrentEpochID"
	CollectorKey                    = "SnapshotCollector"
	FlaggedSnapshotterKey           = "FlaggedSnapshotterKey"
	EpochSubmissionsCountKey        = "EpochSubmissionsCountKey"
	EpochSubmissionsKey             = "EpochSubmissionsKey"
	LastSimulatedSubmissionKey      = "LastSimulatedSubmissionKey"
	LastSnapshotSubmissionKey       = "LastSnapshotSubmissionKey"
	TotalIncomingSubmissionCountKey = "TotalIncomingSubmissionCountKey"
	ActiveSnapshottersForEpoch      = "ActiveSnapshottersForEpoch"
)

// General Constants
const (
	Day          = 24 * time.Hour
	DayBuffer    = 0
	EpochsPerDay = 3
)
