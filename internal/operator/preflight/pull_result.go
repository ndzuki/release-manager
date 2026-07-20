package preflight

import "encoding/json"

func PullResultJSON(result *PullBatchResult) (string, error) {
	if result == nil {
		return "", nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
