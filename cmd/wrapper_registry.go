package cmd

// AnnotationLLMWrapper is the cobra annotation key for commands that invoke LLM sessions.
// Commands with this annotation are recognized as stable session anchors for PID tracking.
const AnnotationLLMWrapper = "ctxloom_llm_wrapper"

// IsLLMWrapper returns true if the command verb is a registered LLM wrapper.
// It looks up the command by name and checks for the LLMWrapper annotation.
func IsLLMWrapper(verb string) bool {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == verb {
			_, hasAnnotation := cmd.Annotations[AnnotationLLMWrapper]
			return hasAnnotation
		}
	}
	return false
}
