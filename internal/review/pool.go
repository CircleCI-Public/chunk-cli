package review

// PoolName is the sidecar pool name for reviews. It keys the persisted pool
// state in .chunk/review-pool.json, so a later run — or a later pass in the
// same run — picks up the same warm sidecars instead of booting new ones.
const PoolName = "review"
