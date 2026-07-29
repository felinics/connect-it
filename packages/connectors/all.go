// Package connectors explicitly registers the Definition of every provider.
// To add a connector: create a directory, add definition.go (and optionally
// managed.go), then add a registration line here.
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/airtable"
	"github.com/memohai/connect-it/packages/connectors/asana"
	"github.com/memohai/connect-it/packages/connectors/box"
	"github.com/memohai/connect-it/packages/connectors/cloudflare"
	"github.com/memohai/connect-it/packages/connectors/datadog"
	"github.com/memohai/connect-it/packages/connectors/dropbox"
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/connectors/gitlab"
	"github.com/memohai/connect-it/packages/connectors/gmail"
	"github.com/memohai/connect-it/packages/connectors/googleads"
	"github.com/memohai/connect-it/packages/connectors/googlecalendar"
	"github.com/memohai/connect-it/packages/connectors/googlechat"
	"github.com/memohai/connect-it/packages/connectors/googledocs"
	"github.com/memohai/connect-it/packages/connectors/googledrive"
	"github.com/memohai/connect-it/packages/connectors/googlepeople"
	"github.com/memohai/connect-it/packages/connectors/googlesheets"
	"github.com/memohai/connect-it/packages/connectors/googleslides"
	"github.com/memohai/connect-it/packages/connectors/hubspot"
	"github.com/memohai/connect-it/packages/connectors/intercom"
	"github.com/memohai/connect-it/packages/connectors/linear"
	"github.com/memohai/connect-it/packages/connectors/monday"
	"github.com/memohai/connect-it/packages/connectors/notion"
	"github.com/memohai/connect-it/packages/connectors/onedrive"
	"github.com/memohai/connect-it/packages/connectors/posthog"
	"github.com/memohai/connect-it/packages/connectors/postman"
	"github.com/memohai/connect-it/packages/connectors/restproviders"
	"github.com/memohai/connect-it/packages/connectors/sentry"
	"github.com/memohai/connect-it/packages/connectors/slack"
	"github.com/memohai/connect-it/packages/connectors/stripe"
	"github.com/memohai/connect-it/packages/connectors/supabase"
	"github.com/memohai/connect-it/packages/connectors/youtube"
	"github.com/memohai/connect-it/packages/core/registry"
)

// RegisterAll registers every Definition.
func RegisterAll(r *registry.Registry) {
	r.MustRegister(airtable.Definition)
	r.MustRegister(asana.Definition)
	r.MustRegister(box.Definition)
	r.MustRegister(cloudflare.Definition)
	r.MustRegister(datadog.Definition)
	r.MustRegister(dropbox.Definition)
	r.MustRegister(github.Definition)
	r.MustRegister(gmail.Definition)
	r.MustRegister(gitlab.Definition)
	r.MustRegister(googleads.Definition)
	r.MustRegister(googlecalendar.Definition)
	r.MustRegister(googlechat.Definition)
	r.MustRegister(googledocs.Definition)
	r.MustRegister(googledrive.Definition)
	r.MustRegister(googlepeople.Definition)
	r.MustRegister(googlesheets.Definition)
	r.MustRegister(googleslides.Definition)
	r.MustRegister(hubspot.Definition)
	r.MustRegister(intercom.Definition)
	r.MustRegister(linear.Definition)
	r.MustRegister(monday.Definition)
	r.MustRegister(notion.Definition)
	r.MustRegister(onedrive.Definition)
	r.MustRegister(posthog.Definition)
	r.MustRegister(postman.Definition)
	for _, def := range restproviders.Definitions {
		r.MustRegister(def)
	}
	r.MustRegister(sentry.Definition)
	r.MustRegister(slack.Definition)
	r.MustRegister(stripe.Definition)
	r.MustRegister(supabase.Definition)
	r.MustRegister(youtube.Definition)
}
