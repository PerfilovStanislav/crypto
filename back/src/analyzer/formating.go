package analyzer

import (
	"fmt"

	"github.com/fatih/color"
)

func (r TaskResult) String() string {
	return spf("%s %s",
		clr(spf("+%6.2f%%", (r.Coef-1)*100), color.FgHiGreen),
		r.Task,
	)
}

func (t Task) String() string {
	return spf("%s %s",
		t.IndicatorsCompare,
		t.TpSlParam,
	)
}

func (c IndicatorsCompare) String() string {
	return spf("[%s] [%s]", c.Indicator1Params, c.Indicator2Params)
}

func (p IndicatorParams) String() string {
	return spf("%s %3s %s",
		clr(spf("%8s", p.Type), color.FgHiYellow),
		p.Source,
		clr(spf("%5.2f", p.Coef), color.FgHiCyan),
	)
}

func (p TpSlParam) String() string {
	return spf("%s %s",
		clr(spf("tp:%4.1f", p.tp), color.FgHiGreen),
		clr(spf("sl:%4.1f", p.sl), color.FgHiRed),
	)
}

func clr(text string, attrs ...color.Attribute) string {
	c := color.New(attrs...)
	c.EnableColor()
	return c.Sprintf("%s", text)
}

func spf(f string, a ...any) string {
	return fmt.Sprintf(f, a...)
}
