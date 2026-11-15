// Package scenario объединяет имя, описание и исполняемый pipeline сценария.
// Сценарий — сущность приложения, поэтому пакет живёт в internal: публичный SDK
// (pkg/execution и pkg/scenario) даёт только движок и готовые кирпичи шагов.
package scenario

import (
	"context"
	"errors"

	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// Scenario объединяет имя, описание и исполняемый pipeline сценария.
type Scenario struct {
	Name        string              // Название сценария в каталоге.
	Description string              // Описание назначения сценария.
	Pipeline    *execution.Pipeline // Подготовленное дерево исполнения.
}

// Run проверяет наличие pipeline и передаёт ему выполнение.
func (s Scenario) Run(ctx context.Context) error {
	if s.Pipeline == nil {
		return errors.New("scenario pipeline is required") // Создаём ошибку с пояснением причины.
	}
	return s.Pipeline.Execute(ctx) // Передаём выполнение pipeline сценария.
}
