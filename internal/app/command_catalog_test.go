package app

import "testing"

// TestCommandCatalogFiltersEntrances 守住帮助与补全共用目录，gateway 不暴露本地操作。
func TestCommandCatalogFiltersEntrances(t *testing.T) {
	tui := Commands(EntranceTUI)
	gateway := Commands(EntranceGateway)
	if len(tui) != 11 || len(gateway) != 9 || len(Commands("unknown")) != 0 {
		t.Fatalf("catalog sizes: tui=%d gateway=%d", len(tui), len(gateway))
	}
	for _, definition := range tui {
		// case: 每项都能反查正式名，并含实际帮助用法和说明。
		lookup, ok := LookupCommand(string(definition.Name))
		if !ok || lookup.Name != definition.Name || definition.Usage == "" || definition.Description == "" || !definition.Supports(EntranceTUI) {
			t.Fatalf("invalid definition: %#v", definition)
		}
		if definition.Name == CommandClear || definition.Name == CommandExit {
			if definition.Supports(EntranceGateway) {
				t.Fatalf("gateway supports local command: %s", definition.Name)
			}
		} else if !definition.Supports(EntranceGateway) {
			t.Fatalf("gateway misses public command: %s", definition.Name)
		}
	}
	for alias, canonical := range map[string]CommandName{"stop": CommandCancel, "quit": CommandExit} {
		// case: 目录反查别名返回规范名称，不额外复制定义。
		got, ok := LookupCommand(alias)
		if !ok || got.Name != canonical {
			t.Fatalf("LookupCommand(%s) = %#v, %v", alias, got, ok)
		}
	}
	if _, ok := LookupCommand("STATUS"); ok {
		t.Fatal("catalog accepted uppercase spelling")
	}
}

// TestCommandCatalogReturnsIndependentSnapshots 守住调用方修改帮助切片不会污染后续解析或其他入口。
func TestCommandCatalogReturnsIndependentSnapshots(t *testing.T) {
	first := Commands(EntranceTUI)
	first[0].Name = "corrupt"
	first[0].Entrances[0] = "corrupt"
	cancel, _ := LookupCommand("cancel")
	cancel.Aliases[0] = "corrupt"
	again, _ := LookupCommand("cancel")
	if Commands(EntranceTUI)[0].Name != CommandHelp || !Commands(EntranceTUI)[0].Supports(EntranceTUI) || again.Aliases[0] != "stop" {
		t.Fatal("catalog snapshot mutation leaked into definitions")
	}
}
