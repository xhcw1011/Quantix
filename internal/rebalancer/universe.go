package rebalancer

// DefaultUniverse is the validated 50-coin liquid mid-cap perp set. The funding factor
// needs mid-cap breadth (top-10-only collapses it), so this is the full liquid list —
// the same universe validated in scripts/xsmom_funding.py and cmd/xsfunding-backtest.
var DefaultUniverse = []string{
	"BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "DOGEUSDT", "ADAUSDT",
	"AVAXUSDT", "LINKUSDT", "DOTUSDT", "TRXUSDT", "LTCUSDT", "BCHUSDT", "ATOMUSDT",
	"UNIUSDT", "ETCUSDT", "XLMUSDT", "NEARUSDT", "APTUSDT", "FILUSDT", "INJUSDT",
	"OPUSDT", "ARBUSDT", "SUIUSDT", "TIAUSDT", "SEIUSDT", "RUNEUSDT", "AAVEUSDT",
	"LDOUSDT", "WLDUSDT", "ALGOUSDT", "VETUSDT", "ICPUSDT", "HBARUSDT", "SANDUSDT",
	"MANAUSDT", "AXSUSDT", "GALAUSDT", "CHZUSDT", "EOSUSDT", "EGLDUSDT", "THETAUSDT",
	"GRTUSDT", "IMXUSDT", "STXUSDT", "ORDIUSDT", "PYTHUSDT", "JUPUSDT", "DYDXUSDT", "CRVUSDT",
}
