// Package idgen 生成形如 swift-otter-42 的词组 id：
// 形容词-名词-序号，序号单调递增保证全局唯一，词组提供可读性。
package helper

import (
	"fmt"
	"math/rand"
	"sync"
)

var adjectives = []string{
	"swift", "calm", "bold", "quiet", "lucky", "brave", "eager", "gentle",
	"proud", "wise", "witty", "zesty", "amber", "bright", "clever", "dapper",
	"fancy", "glad", "hazy", "icy", "jolly", "keen", "lively", "mellow",
	"noble", "olive", "plush", "quirky", "rapid", "shiny", "tidy", "urban",
	"vivid", "warm", "young", "zippy", "cosmic", "dusty", "electric", "frosty",
	"golden", "hidden", "jade", "kind", "lunar", "misty", "novel", "opal",
	"peppy", "royal", "silver", "tender", "urban", "velvet", "wild", "zonal",
	"arctic", "breezy", "cozy", "deft", "elated", "festive", "giddy", "humble",
}

var nouns = []string{
	"otter", "falcon", "tiger", "panda", "raven", "whale", "fox", "wolf",
	"hawk", "lynx", "koala", "tapir", "gecko", "heron", "ibis", "jaguar",
	"kiwi", "lemur", "moose", "narwhal", "okapi", "quail", "seal", "toad",
	"viper", "wombat", "yak", "zebra", "beetle", "cicada", "dragonfly", "earwig",
	"firefly", "guppy", "hedgehog", "impala", "jackal", "kudu", "lark", "mantis",
	"newt", "oriole", "puffin", "quetzal", "roadrunner", "starling", "toucan", "urchin",
	"vole", "weasel", "xerus", "yeast", "zorro", "alpaca", "badger", "capybara",
	"dolphin", "emu", "flamingo", "gorilla", "hamster", "iguana", "jellyfish", "kangaroo",
}

// Generator 分配全局唯一的词组 id。
type Generator struct {
	mu     sync.Mutex
	used   map[string]bool
	nextID int
	rnd    *rand.Rand
}

func NewIDGen() *Generator {
	return &Generator{
		used:   make(map[string]bool),
		nextID: 1,
		rnd:    rand.New(rand.NewSource(rand.Int63())),
	}
}

// Next 返回一个新的 id，形如 "swift-otter-42"。
// 序号保证唯一性；随机词组提供可辨识度，重复则重摇。
func (g *Generator) Next() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := g.nextID
	g.nextID++
	for {
		id := fmt.Sprintf("%s-%s-%d",
			adjectives[g.rnd.Intn(len(adjectives))],
			nouns[g.rnd.Intn(len(nouns))], n)
		if !g.used[id] {
			g.used[id] = true
			return id
		}
	}
}
