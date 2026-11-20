package beichen

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
)

type CredentialHealth struct {
	AvailableDimensions int             `json:"available_dimensions"`
	Credential          HealthDimension `json:"credential"`
	Catalog             HealthDimension `json:"catalog"`
	Pricing             HealthDimension `json:"pricing"`
	Probe               HealthDimension `json:"probe"`
}

type HealthDimension struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

type keyModelCounts struct {
	total, priced, probeable, checked, passed, authRejected int
}

func credentialModelCounts(models []Model) map[uuid.UUID]keyModelCounts {
	counts := make(map[uuid.UUID]keyModelCounts)
	for _, model := range models {
		count := counts[model.CredentialID]
		count.total++
		if model.PriceCovered {
			count.priced++
		}
		for _, protocol := range model.protocols {
			if _, ok := catalog.TokenProbeEndpoint(protocol); ok {
				count.probeable++
				break
			}
		}
		if model.lastProbeAt != nil {
			count.checked++
		}
		if model.probeState == "passed" {
			count.passed++
		}
		if model.credentialRejected {
			count.authRejected++
		}
		counts[model.CredentialID] = count
	}
	return counts
}

func buildCredentialHealth(credential CredentialSummary, counts keyModelCounts, enabled, configured bool, revision time.Time, datasetErrors map[string]*string) CredentialHealth {
	const available, unavailable, pending = "available", "unavailable", "pending"
	if !enabled {
		disabled := HealthDimension{unavailable, "供应商或 Key 已停用"}
		return CredentialHealth{Credential: disabled, Catalog: disabled, Pricing: disabled, Probe: disabled}
	}
	health := CredentialHealth{
		Credential: HealthDimension{available, "Key 已配置并启用"},
		Catalog:    HealthDimension{pending, "等待同步当前 Key 的模型目录"},
		Pricing:    HealthDimension{pending, "等待同步官网报价或设置手动价"},
		Probe:      HealthDimension{pending, "等待首次探活"},
	}
	catalogError := datasetErrors["catalog"]
	switch {
	case !configured:
		health.Credential = HealthDimension{unavailable, "Key 尚未配置"}
	case counts.checked > 0 && counts.authRejected == counts.checked,
		catalogError != nil && (*catalogError == "北辰接口返回 HTTP 401" || *catalogError == "北辰接口返回 HTTP 403"):
		health.Credential = HealthDimension{unavailable, "Key 无法读取或被北辰拒绝"}
	}
	switch {
	case catalogError != nil:
		health.Catalog = HealthDimension{unavailable, *catalogError}
	case credential.CatalogLastSuccess != nil && !credential.CatalogLastSuccess.Before(revision):
		health.Catalog = HealthDimension{available, fmt.Sprintf("目录已读取，包含 %d 个模型", counts.total)}
	}
	switch {
	case counts.priced > 0:
		health.Pricing = HealthDimension{available, fmt.Sprintf("%d/%d 个模型有可计算报价", counts.priced, counts.total)}
	case datasetErrors["prices"] != nil:
		health.Pricing = HealthDimension{unavailable, *datasetErrors["prices"]}
	case credential.PricesLastSuccess != nil:
		health.Pricing = HealthDimension{unavailable, "当前目录内没有可计算报价"}
	}
	switch {
	case counts.probeable == 0 && counts.total > 0:
		health.Probe.Message = "当前模型没有可限制为 1 个输出 Token 的协议"
	case counts.checked > 0:
		state := pending
		switch {
		case counts.passed == counts.probeable:
			state = available
		case counts.passed < counts.checked:
			state = unavailable
		}
		health.Probe = HealthDimension{state, fmt.Sprintf("%d/%d 个模型至少有一个协议通过", counts.passed, counts.probeable)}
	}
	for _, dimension := range []HealthDimension{health.Credential, health.Catalog, health.Pricing, health.Probe} {
		if dimension.State == available {
			health.AvailableDimensions++
		}
	}
	return health
}
