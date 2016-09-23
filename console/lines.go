package console

import (
	"fmt"
	"regexp"

	"github.com/fatih/color"
)

var regexpMpManTick = regexp.MustCompile(`^networkTick\((\d+)\) mapTick\((\d+)\) (.*)$`)
var regexpMpManTickState = regexp.MustCompile(`^changing state from\(([a-zA-Z_]+)\) to\(([a-zA-Z_]+)\)$`)

func parseMpManagerLine(match []string, match2 []string) string {
	var string3 string = match2[2]
	if match3 := regexpMpManTick.FindStringSubmatch(match2[2]); match3 != nil {
		var string4 string = match3[3]
		if match4 := regexpMpManTickState.FindStringSubmatch(match3[3]); match4 != nil {
			string4 = fmt.Sprintf("%s%s%s%s%s",
				colorDebug.SprintFunc()("changing state from("),
				color.New(color.FgGreen, color.Bold).SprintFunc()(match4[1]),
				colorDebug.SprintFunc()(") to("),
				color.New(color.FgGreen, color.Bold).SprintFunc()(match4[2]),
				colorDebug.SprintFunc()(")"),
			)
		}
		string3 = fmt.Sprintf("%s%s%s%s%s%s",
			colorDebug.SprintFunc()("networkTick("),
			colorStdout.SprintFunc()(match3[1]),
			colorDebug.SprintFunc()(") mapTick("),
			colorStdout.SprintFunc()(match3[2]),
			colorDebug.SprintFunc()(") "),
			string4,
		)
	}
	return string3
}
