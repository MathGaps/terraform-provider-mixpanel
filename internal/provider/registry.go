// Hand-maintained provider resource/data-source registry. Originally
// bootstrapped by a gen_stubs.py script that is no longer part of the
// generation pipeline; gen/crudgen.py (driven by gen/regen.sh, see
// gen/README.md) does NOT touch this file — add the constructor for each new
// entity here by hand.

package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// providerResources lists every resource constructor registered by the provider.
func providerResources() []func() resource.Resource {
	return []func() resource.Resource{
		NewAgentFlowResource,
		NewAnnotationResource,
		NewBehaviorResource,
		NewBookmarkResource,
		NewBusinessContextResource,
		NewCanvasResource,
		NewCohortResource,
		NewConnectorResource,
		NewCustomAlertResource,
		NewCustomEventResource,
		NewCustomPropertyResource,
		NewCustomRoleResource,
		NewDashboardResource,
		NewDashboardContentResource,
		NewDataGovernanceSettingsResource,
		NewDataGroupResource,
		NewDatasetResource,
		NewEventDropFilterResource,
		NewEmailDigestResource,
		NewEventDefinitionResource,
		NewExperimentResource,
		NewFeatureFlagResource,
		NewFormulaResource,
		NewHeatMapResource,
		NewHeatMapCollectionResource,
		NewLexiconTagResource,
		NewLookupTableResource,
		NewMetricResource,
		NewOrgRequestAccessSettingsResource,
		NewOrgSessionSettingsResource,
		NewPlaylistResource,
		NewProjectResource,
		NewPropertyDefinitionResource,
		NewRollupProjectResource,
		NewServiceAccountProjectResource,
		NewServiceAccountResource,
		NewSparkSettingsResource,
		NewTeamResource,
		NewThemeResource,
		NewTwofactorSettingsResource,
		NewUserProjectRoleResource,
		NewWarehouseSourceResource,
		NewWebhookResource,
		NewWorkspaceResource,
	}
}

// providerDataSources lists every data-source constructor registered by the provider.
func providerDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewAgentFlowDataSource,
		NewAnnotationDataSource,
		NewBehaviorDataSource,
		NewBookmarkDataSource,
		NewCanvasDataSource,
		NewCohortDataSource,
		NewConnectorDataSource,
		NewCustomAlertDataSource,
		NewCustomEventDataSource,
		NewCustomPropertyDataSource,
		NewCustomRoleDataSource,
		NewDashboardDataSource,
		NewDatasetDataSource,
		NewEmailDigestDataSource,
		NewEventDefinitionDataSource,
		NewExperimentDataSource,
		NewFeatureFlagDataSource,
		NewHeatMapDataSource,
		NewHeatMapCollectionDataSource,
		NewMetricDataSource,
		NewOrgRequestAccessSettingsDataSource,
		NewOrgSessionSettingsDataSource,
		NewPlaylistDataSource,
		NewProjectOutgoingIntegrationDataSource,
		NewPropertyDefinitionDataSource,
		NewRollupProjectDataSource,
		NewServiceAccountDataSource,
		NewSparkSettingsDataSource,
		NewTagDataSource,
		NewThemeDataSource,
		NewTwofactorSettingsDataSource,
		NewWarehouseSourceDataSource,
		NewWorkspaceDataSource,
		// Plural "list" data sources (GREEN-12): bulk id + composite import-id discovery.
		NewAgentFlowListDataSource,
		NewAnnotationListDataSource,
		NewCohortListDataSource,
		NewCustomAlertListDataSource,
		NewCustomEventListDataSource,
		NewCustomPropertyListDataSource,
		NewCustomRoleListDataSource,
		NewEmailDigestListDataSource,
		NewExperimentListDataSource,
		NewFeatureFlagListDataSource,
		NewHeatMapListDataSource,
		NewPlaylistListDataSource,
		NewSchemaGraphDataSource,
	}
}
