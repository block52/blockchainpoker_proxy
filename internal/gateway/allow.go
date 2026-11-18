package gateway

// DefaultAllow is the deny-by-default set of upstream events a b52 UI may invoke on a user's
// behalf. Anything touching money movement, credentials, email/MFA, affiliates or admin is
// deliberately absent: the gateway holds the account secret, so the UI must never reach those.
// Action event names (fold/call/...) are unconfirmed against live traffic: see issue #2.
var DefaultAllow = setOf(
	// table lifecycle
	"join", "sit", "stand", "sitIn", "topUp", "getMinBuyIn", "useBankedTime",
	// betting (names to be confirmed from a capture)
	"fold", "call", "check", "raise", "allin",
	"sendChat",
	// read-only
	"gettables", "getsitngos", "gettournaments", "getHighlightedTables", "getHigherStakesTable",
	"getTableMetrics", "getMyTables", "getProfile", "getPlayersOnline", "getBlindSchedule",
	"getTournamentSummary", "getTournamentPlayers", "getTournamentPayouts", "getRecentHands",
)

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}
