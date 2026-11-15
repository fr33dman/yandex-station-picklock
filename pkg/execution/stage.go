// Package execution is the public engine of the scenario SDK: stage config
// (critical, optional), the job tree and their observable snapshots. Scenario
// authors usually start from pkg/scenario constructors and ready-made steps,
// and may describe Stage values by hand when the SDK does not fit.
package execution

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
)

// Stage описывает один шаг; состояние выполнения и восстановление хранит его Job.
// Готовые конструкторы NewStage, Optional и BestEffort подставляют безопасные
// умолчания; поля можно заполнять и вручную.
type Stage struct {
	Name        string                      // Название шага для статуса и ошибок.
	Description string                      // Описание действий шага для UI.
	IsCritical  bool                        // Сбой шага останавливает весь pipeline.
	IsOptional  bool                        // Пользователь выбирает, запускать шаг или пропустить его.
	Executive   func(context.Context) error // Действие шага; отмена передаётся через контекст.
}

// NewStage создаёт критичный шаг: его сбой останавливает весь pipeline.
// Это рекомендуемый конструктор; BestEffort снимает критичность.
func NewStage(name, description string, executive func(context.Context) error) Stage {
	return Stage{Name: name, Description: description, IsCritical: true, Executive: executive}
}

// BestEffort оставляет сбой шага в снимке и графе, но не останавливает pipeline.
func (s Stage) BestEffort() Stage {
	s.IsCritical = false
	return s
}

// Optional даёт пользователю выбор: запустить шаг или пропустить его.
// Решение принимает OptionalDecision из контекста; без него шаг выполняется.
func (s Stage) Optional() Stage {
	s.IsOptional = true
	return s
}

// Execute запускает шаг, учитывает отмену и превращает панику в ошибку со стеком.
// Значения контекста передаются без изменений; общий изменяемый объект требует синхронизации.
func (s Stage) Execute(ctx context.Context) (err error) {
	defer func() { // При выходе перехватываем панику и добавляем имя шага к ошибке.
		if value := recover(); value != nil { // Перехватываем панику для возврата обычной ошибки.
			err = fmt.Errorf("panic: %v\n%s", value, debug.Stack()) // Добавляем пояснение к возвращаемой ошибке.
		}
		if err != nil {
			err = fmt.Errorf("stage %q: %w", s.Name, err) // Добавляем пояснение к возвращаемой ошибке.
		}
	}()
	if err := ctx.Err(); err != nil { // Проверяем отмену или истечение срока контекста.
		return err
	}
	if s.Executive == nil {
		return errors.New("executive is required") // Создаём ошибку с пояснением причины.
	}
	err = s.Executive(ctx)            // Выполняем действие шага с исходным контекстом.
	return withCancellation(ctx, err) // Сохраняем ошибку операции вместе с причиной отмены.
}
