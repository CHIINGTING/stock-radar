package strategy

import "sort"

// SectorScore holds the aggregated strength of a Taiwan stock sector.
type SectorScore struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
	Flow  string `json:"flow"`  // 流入 / 持平 / 流出
	Count int    `json:"count"` // number of stocks in watchlist for this sector
}

// sectorMap maps Taiwan stock codes to their primary sector.
// Stocks not in the map fall under "其他".
var sectorMap = map[string]string{
	// 記憶體 (Memory / Flash / DRAM)
	"2337": "記憶體", // 旺宏  NOR Flash
	"2344": "記憶體", // 華邦電 NOR / NAND Flash
	"2408": "記憶體", // 南亞科 DRAM

	// 半導體製造 (Foundry / Wafer / Test)
	"2303": "半導體", // 聯電  foundry
	"2330": "半導體", // 台積電 foundry
	"2401": "半導體", // 凌陽  fabless
	"5483": "半導體", // 中美晶 silicon wafer
	"6182": "半導體", // 合晶   silicon wafer
	"3264": "半導體", // 欣銓   IC test

	// IC設計 (IC Design)
	"2379": "IC設計", // 瑞昱 Realtek
	"8299": "IC設計", // 群聯 Phison

	// 伺服器 / 電子製造 (Server / EMS)
	"2356": "伺服器", // 英業達
	"2374": "電子製造", // 佳能

	// 面板 (Display)
	"2409": "面板", // 友達

	// 工業機械 (Industrial Equipment)
	"1582": "工業機械", // 信錦
	"1504": "工業機械", // 東元

	// 電子零組件 (Electronic Components)
	"3048": "電子零組件", // 益登
	"3013": "電子零組件", // 晟銘電
	"2328": "電子零組件", // 廣宇
	"2419": "電子零組件", // 仲奇

	// 電源 (Power Supply)
	"3015": "電源", // 全漢
	"4532": "電源", // 瑞智
}

// SectorOf returns the sector name for a stock code.
func SectorOf(code string) string {
	if s, ok := sectorMap[code]; ok {
		return s
	}
	return "其他"
}

// RankSectors computes sector rankings from a map of code → scanner score.
// Returns sectors sorted by average score (highest first).
func RankSectors(scores map[string]int) []SectorScore {
	totals := make(map[string]int)
	counts := make(map[string]int)
	for code, score := range scores {
		s := SectorOf(code)
		if s == "其他" {
			continue
		}
		totals[s] += score
		counts[s]++
	}

	out := make([]SectorScore, 0, len(totals))
	for name, total := range totals {
		avg := total / counts[name]
		flow := "持平"
		if avg >= 65 {
			flow = "流入"
		} else if avg <= 40 {
			flow = "流出"
		}
		out = append(out, SectorScore{
			Name:  name,
			Score: avg,
			Flow:  flow,
			Count: counts[name],
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// SectorRank returns the 1-based rank of a sector name in the provided ranking slice.
// Returns 0 if not found.
func SectorRank(name string, sectors []SectorScore) int {
	for i, s := range sectors {
		if s.Name == name {
			return i + 1
		}
	}
	return 0
}

// SectorFlow returns the flow string for a sector name.
// Returns "持平" if not found.
func SectorFlow(name string, sectors []SectorScore) string {
	for _, s := range sectors {
		if s.Name == name {
			return s.Flow
		}
	}
	return "持平"
}
