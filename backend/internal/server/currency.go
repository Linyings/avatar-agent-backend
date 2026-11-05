package server

import "math"

const usdToCNYRateForWeChat = 6.81

func usdToWeChatCNYAmount(usd float64) float64 {
	return math.Round(usd*usdToCNYRateForWeChat*100) / 100
}
