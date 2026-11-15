package main

import "github.com/dream-until-dawn/futures-tickflow-go/archive/batch"

// catalogEvidenceCandidates 仅取新发现的官方公报；不重试首页、旧年度包或失败原件。
func catalogEvidenceCandidates() []batch.Job {
	return []batch.Job{{ID: "followup-catalog-journal2019", URL: "https://www.czce.com.cn/cn/rootfiles/2020/01/13/1572882898738930-1572882898769401.pdf", Format: "pdf", Version: "official-journal-candidate-not-annual-calendar"}}
}
