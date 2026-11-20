# Messages for the crate's notices. Each id is a notice's code, which begins
# with the crate's name; the variables are its args.

engine-opencode-no-model = opencode will choose its own model for agent { $agent }: agents.{ $agent }.engines.opencode.model is not set, and opencode's own choice may not support tool use
engine-opencode-no-model-fix = ctxloom agent edit { $agent } --engine opencode --model <provider/model>
