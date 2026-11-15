package execution

import (
	"context"
	"errors"
	"sync"
)

type (
	// Job описывает узел дерева выполнения; запуск и пропуск доступны только внутри execution.
	Job interface {
		Snapshot() JobSnapshot         // Snapshot возвращает состояние узла и его ветвей.
		execute(context.Context) error // execute исполняет узел с общим контекстом.
		skip()                         // skip пропускает ещё не начатые части узла.
		critical() bool                // critical сообщает, останавливает ли сбой узла pipeline.
	}
	// SingleJob выполняет один Stage и при ошибке запускает запасную задачу.
	SingleJob struct {
		state      runState // Общий результат задачи после восстановления.
		stage      Stage    // Исходный шаг и его действие.
		stageState runState // Результат исходного шага до восстановления.
		fallback   Job      // Запасная задача; nil отключает восстановление.
	}
	// ParallelJobs запускает дочерние задачи параллельно и ждёт завершения каждой.
	ParallelJobs struct {
		state    runState // Общий результат параллельной группы.
		jobs     []Job    // Дочерние задачи, исполняемые в отдельных горутинах.
		fallback Job      // Общий запасной путь после завершения всех ветвей.
	}
)

// NewSingleJob создаёт задачу с запасным путём. Успешное восстановление сохраняет ошибку исходного шага в снимке.
func NewSingleJob(stage Stage, fallback Job) *SingleJob {
	return &SingleJob{
		state: runState{status: Pending}, stage: stage,
		stageState: runState{status: Pending}, fallback: fallback,
	}
}

// NewParallelJobs копирует список параллельных задач и задаёт общий запасной путь.
func NewParallelJobs(jobs []Job, fallback Job) *ParallelJobs {
	return &ParallelJobs{
		state: runState{status: Pending}, jobs: append([]Job(nil), jobs...), fallback: fallback, // Копируем список, не разделяя его массив с вызывающим кодом.
	}
}

// execute запускает шаг с метаданными задачи, затем обновляет состояние с учётом восстановления.
func (j *SingleJob) execute(ctx context.Context) error {
	_, _, id := j.state.snapshot()                                                                                  // Читаем защищённое состояние и идентификатор задачи.
	ctx = context.WithValue(ctx, taskKey{}, TaskInfo{ID: id, Name: j.stage.Name, Description: j.stage.Description}) // Помещаем ID, имя и описание задачи в контекст её шага.
	if j.stage.IsOptional {                                                                                         // Необязательный шаг запускается только с согласия пользователя.
		run, err := decideOptional(ctx, j.stage) // Спрашиваем, запускать шаг или пропустить его.
		if err != nil {
			j.stageState.skip()      // Шаг не начинался, поэтому показываем его пропущенным.
			j.state.finish(ctx, err) // Сохраняем причину отказа от запуска.
			return err
		}
		if !run {
			j.stageState.skip() // Помечаем исходный шаг пропущенным по выбору пользователя.
			j.state.skip()      // Помечаем ещё не начатую задачу пропущенной.
			return nil
		}
	}
	if err := j.state.start(); err != nil { // Помечаем задачу выполняемой и исключаем повторный запуск.
		return err
	}
	_ = j.stageState.start()                // Помечаем исходный шаг выполняемым.
	err := j.stage.Execute(ctx)             // Выполняем исходный шаг задачи.
	j.stageState.finish(ctx, err)           // Сохраняем результат исходного шага до восстановления.
	err = runFallback(ctx, err, j.fallback) // Пробуем восстановление, если ошибка допускает его запуск.
	j.state.finish(ctx, err)                // Сохраняем итог задачи с учётом восстановления.
	return err
}

// execute запускает дочерние задачи в горутинах; даже при ошибке или отмене ждёт всех.
func (j *ParallelJobs) execute(ctx context.Context) error {
	if err := j.state.start(); err != nil { // Помечаем задачу выполняемой и исключаем повторный запуск.
		return err
	}
	results := make([]error, len(j.jobs)) // Выделяем отдельную ячейку ошибки для каждой ветви.
	var wg sync.WaitGroup                 // Ожидание завершения всех параллельных ветвей.
	for i, job := range j.jobs {
		wg.Go(func() { // Горутина записывает результат своей ветви в отдельную ячейку.
			results[i] = job.execute(ctx) // Выполняем дочернюю задачу с общим контекстом.
		})
	}

	wg.Wait()                                                    // Ждём все дочерние задачи, включая завершение после отмены.
	err := runFallback(ctx, errors.Join(results...), j.fallback) // Пробуем восстановление, если ошибка допускает его запуск.
	j.state.finish(ctx, err)                                     // Сохраняем итог задачи с учётом восстановления.
	return err
}

// skip помечает ещё не начатые шаг и запасную задачу пропущенными.
func (j *SingleJob) skip() {
	j.stageState.skip() // Пропускаем ещё не начатый шаг.
	if j.fallback != nil {
		j.fallback.skip() // Пропускаем запасную ветвь задачи.
	}
	j.state.skip() // Помечаем ещё не начатую задачу пропущенной.
}

// skip рекурсивно пропускает ещё не начатые ветви группы.
func (j *ParallelJobs) skip() {
	for _, job := range j.jobs {
		job.skip() // Пропускаем ещё не начатую дочернюю задачу.
	}
	if j.fallback != nil {
		j.fallback.skip() // Пропускаем запасную ветвь задачи.
	}
	j.state.skip() // Помечаем ещё не начатую задачу пропущенной.
}

// Snapshot возвращает состояния задачи, исходного шага и запасного пути.
func (j *SingleJob) Snapshot() JobSnapshot {
	status, err, id := j.state.snapshot()               // Читаем защищённое состояние и идентификатор задачи.
	stageStatus, stageErr, _ := j.stageState.snapshot() // Читаем состояние и ошибку исходного шага.
	snapshot := JobSnapshot{                            // Отдельный снимок состояния для вызывающего кода.
		ID: id, Status: status, Error: err,
		Stage: &StageSnapshot{
			Name: j.stage.Name, Description: j.stage.Description,
			Status: stageStatus, Error: stageErr,
			IsCritical: j.stage.IsCritical, IsOptional: j.stage.IsOptional,
		},
	}
	if j.fallback != nil {
		fallback := j.fallback.Snapshot() // Получаем снимок запасной ветви.
		snapshot.Fallback = &fallback
	}
	return snapshot
}

// Snapshot собирает состояние группы, всех дочерних задач и запасного пути.
func (j *ParallelJobs) Snapshot() JobSnapshot {
	status, err, id := j.state.snapshot()                                                               // Читаем защищённое состояние и идентификатор задачи.
	snapshot := JobSnapshot{ID: id, Status: status, Error: err, Jobs: make([]JobSnapshot, len(j.jobs))} // Выделяем независимый список снимков задач.
	for i, job := range j.jobs {
		snapshot.Jobs[i] = job.Snapshot() // Получаем снимок дочерней задачи.
	}
	if j.fallback != nil {
		fallback := j.fallback.Snapshot() // Получаем снимок запасной ветви.
		snapshot.Fallback = &fallback
	}
	return snapshot
}

// critical сообщает, останавливает ли сбой шага весь pipeline.
func (j *SingleJob) critical() bool { return j.stage.IsCritical }

// critical сообщает, останавливает ли сбой группы весь pipeline: достаточно одной критичной ветви.
func (j *ParallelJobs) critical() bool {
	for _, job := range j.jobs {
		if job.critical() {
			return true
		}
	}
	return false
}

// runFallback восстанавливает задачу после ошибки; при отмене запасной путь пропускается.
func runFallback(ctx context.Context, err error, fallback Job) error {
	err = withCancellation(ctx, err) // Сохраняем ошибку операции вместе с причиной отмены.
	if fallback != nil {
		if err != nil && ctx.Err() == nil { // Восстановление запускается только при сбое без отмены.
			if fallbackErr := fallback.execute(ctx); fallbackErr != nil { // Выполняем запасную задачу после сбоя.
				err = errors.Join(err, fallbackErr) // Объединяем ошибки, сохраняя каждую причину.
			} else {
				err = nil
			}
		} else {
			fallback.skip() // Помечаем ненужное восстановление пропущенным.
		}
	}
	return withCancellation(ctx, err) // Сохраняем ошибку операции вместе с причиной отмены.
}
