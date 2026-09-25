// Package vocabowner declares the vocabulary rule's fixture vocabularies.
// There are several, each with several members, so a fact that encoded in map
// iteration order would differ between two encodings almost every time.
package vocabowner

// Mode is a closed vocabulary.
type Mode string

// Mode's members.
const (
	Fast  Mode = "fast"
	Slow  Mode = "slow"
	Eager Mode = "eager"
	Lazy  Mode = "lazy"
)

// Tier is a closed vocabulary.
type Tier string

// Tier's members.
const (
	Gold   Tier = "gold"
	Silver Tier = "silver"
	Bronze Tier = "bronze"
)

// Shape is a closed vocabulary.
type Shape string

// Shape's members.
const (
	Round  Shape = "round"
	Square Shape = "square"
	Flat   Shape = "flat"
)
