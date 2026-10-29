package history

// NewWithBatchSize returns a service that reads batch snapshots at a time, so
// tests can cover ranges that need several reads without storing thousands
// of snapshots.
func NewWithBatchSize(store Store, batch int) *Service {
	return &Service{store: store, batch: batch}
}
