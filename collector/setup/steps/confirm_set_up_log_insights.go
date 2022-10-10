package steps

import (
	"errors"

	survey "github.com/AlecAivazis/survey/v2"
	"github.com/guregu/null"
	s "github.com/pganalyze/collector/setup/state"
)

var ConfirmSetUpLogInsights = &s.Step{
	ID:          "confirm_set_up_log_insights",
	Description: "Confirm whether to set up the optional Log Insights feature",
	Check: func(state *s.SetupState) (bool, error) {
		return state.Inputs.ConfirmSetUpLogInsights.Valid || state.PGAnalyzeSection.HasKey("db_log_location"), nil
	},
	Run: func(state *s.SetupState) error {
		if state.Inputs.Scripted {
			return errors.New("skip_log_insights value must be specified")
		}
		state.Log(`
Basic setup is almost complete. You can complete it now, or proceed to
configuring the optional Log Insights feature. Log Insights will require
specifying your database log file (we may be able to detect this), and
may require changes to some logging-related settings.

Setting up Log Insights is required for the Automated EXPLAIN feature.

Learn more at https://pganalyze.com/log-insights
`)
		var setUpLogInsights bool
		err := survey.AskOne(&survey.Confirm{
			Message: "Proceed to configuring optional Log Insights feature?",
			Default: false,
		}, &setUpLogInsights)
		if err != nil {
			return err
		}
		state.Inputs.ConfirmSetUpLogInsights = null.BoolFrom(setUpLogInsights)

		return nil
	},
}
