package handler

import (
	"net/http"

	"github.com/zhangyu/windsurfapi-go/internal/modelaccess"
	"github.com/zhangyu/windsurfapi-go/internal/models"
)

// ModelsHandler returns available models in OpenAI format
func ModelsHandler(access *modelaccess.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if access == nil {
			writeJSONNoEscape(w, models.ToOpenAIModelList())
			return
		}
		writeJSONNoEscape(w, models.ToOpenAIModelListFiltered(func(m models.Model) bool {
			return access.IsVisible(m.ID)
		}))
	}
}
