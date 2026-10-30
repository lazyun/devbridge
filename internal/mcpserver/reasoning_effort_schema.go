package mcpserver

// Tool schemas are protocol-stable syntax contracts. Enabled inventory is
// deliberately absent: runtime admission reads the current Core snapshot.
const modelIDSchema = `{"type":"string","minLength":1,"maxLength":128,"pattern":"^[^,\\s]+$"}`

const reasoningEffortSchema = `{"type":"string","enum":["low","medium","high","xhigh","max","ultra"]}`

func reviewStartInputSchema() string { return externalReviewStartSchema }
