package reasoning

import (
	"log"
	"os"
)

// fireRulesDebug logs FireRules inputs/candidates when PENSIERO_FIRE_DEBUG=1, for
// diagnosing why management rules do or don't fire.
var fireRulesDebug = os.Getenv("PENSIERO_FIRE_DEBUG") == "1"

func fireLog(format string, args ...any) { log.Printf(format, args...) }
