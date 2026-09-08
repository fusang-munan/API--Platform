package catalog

// Provider catalog sync pipeline:
//
//	Refresh only (Key save / Key activate):
//	  fetch upstream → provider_model_catalog → ReconcileProviderBindingRoutes
//
//	Full apply (new provider, base URL change, Reset Catalog):
//	  fetch upstream per Key → provider_model_catalog → MaterializeCredentialModels per Key → ReconcileProviderBindingRoutes
//
// HTTP route probe (teacher 新增路由) is separate and never runs during catalog refresh.
//
// Model status is teacher-controlled; Reset Catalog is the only path that clears governance.
