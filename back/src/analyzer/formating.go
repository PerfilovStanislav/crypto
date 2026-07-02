package analyzer

import (
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
		clr(spf("tp:%2.0f", p.tp), color.FgHiGreen),
		clr(spf("sl:%2.0f", p.sl), color.FgHiRed),
	)
}
