package coord

// MaxInlineBodyBytes bounds the part of one message body delivered inline.
const MaxInlineBodyBytes = 8 << 10

const overflowMarkerPhrase = "keep reading for more detail"

func inlineHead(body string, limit int) (string, bool) { return body, false }

func overflowArtifactID(shaHex string) string { return "" }

func overflowMarker(holder, artifactID string, size int) string { return "" }

func (c *Coordinator) boundBody(holder, body string) (string, error) { return body, nil }
