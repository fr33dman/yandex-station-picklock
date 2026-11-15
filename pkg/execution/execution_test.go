package execution_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// job создаёт тестовую задачу с указанным действием и запасным путём.
func job(name string, executive func(context.Context) error, fallback execution.Job) *execution.SingleJob {
	return execution.NewSingleJob(execution.NewStage(name, "", executive), fallback) // Создаём критичную задачу с тестовым действием.
}

// parallel объединяет тестовые задачи в параллельную группу.
func parallel(jobs ...execution.Job) *execution.ParallelJobs {
	return execution.NewParallelJobs(jobs, nil) // Объединяем тестовые задачи для параллельного запуска.
}

// pipeline создаёт pipeline и завершает тест при ошибке его сборки.
func pipeline(t *testing.T, jobs ...execution.Job) *execution.Pipeline {
	t.Helper()                               // Относим ошибки вспомогательной функции к вызывающему тесту.
	p, err := execution.NewPipeline(jobs...) // Проверяем и собираем тестовое дерево задач.
	if err != nil {
		t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
	}
	return p
}

// TestSequentialJobsShareContext проверяет порядок задач и передачу общих данных через контекст.
func TestSequentialJobsShareContext(t *testing.T) {
	// key изолирует тестовые значения контекста от чужих ключей.
	type key struct{}

	values := &sync.Map{}                                // Общее потокобезопасное хранилище между шагами.
	ctx := context.WithValue(t.Context(), key{}, values) // Добавляем данные в контекст без изменения родителя.
	var order []string                                   // Фактический порядок выполнения последовательных шагов.
	p := pipeline(t,                                     // Собираем pipeline из тестовых задач.
		job("produce", func(ctx context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			ctx.Value(key{}).(*sync.Map).Store("address", "192.168.1.2") // Записываем данные в синхронизированное хранилище контекста.
			order = append(order, "produce")                             // Дополняем список очередным элементом.
			return nil
		}, nil),
		job("consume", func(ctx context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			value, ok := ctx.Value(key{}).(*sync.Map).Load("address") // Читаем данные, переданные предыдущим шагом.
			if !ok || value != "192.168.1.2" {
				return fmt.Errorf("shared address: %v", value) // Добавляем пояснение к возвращаемой ошибке.
			}
			order = append(order, "consume") // Дополняем список очередным элементом.
			return nil
		}, nil),
	)
	if err := p.Execute(ctx); err != nil { // Запускаем pipeline с тестовым контекстом.
		t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
	}
	if !reflect.DeepEqual(order, []string{"produce", "consume"}) { // Сравниваем структуру результата с ожидаемой.
		t.Fatalf("execution order: %v", order) // Завершаем тест с диагностикой полученных значений.
	}
	snapshot := p.Snapshot()                                               // Читаем снимок состояния pipeline.
	if snapshot.Status != execution.Succeeded || len(snapshot.Jobs) != 2 { // Проверяем количество доступных элементов.
		t.Fatalf("pipeline snapshot: %+v", snapshot) // Завершаем тест с диагностикой полученных значений.
	}
	for _, job := range snapshot.Jobs {
		if job.Status != execution.Succeeded || job.Stage.Status != execution.Succeeded {
			t.Fatalf("job snapshot: %+v", job) // Завершаем тест с диагностикой полученных значений.
		}
	}
	if err := p.Execute(ctx); !errors.Is(err, execution.ErrAlreadyStarted) { // Повторный запуск требует нового дерева задач.
		t.Fatalf("repeated run: %v", err) // Завершаем тест с диагностикой полученных значений.
	}
}

// TestNestedParallelJobsWaitForEveryChild проверяет ожидание всех вложенных параллельных задач.
func TestNestedParallelJobsWaitForEveryChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // Проверяем конкурентный код с управляемым временем.
		entered := make(chan string, 3)            // Канал сообщает тесту, что шаг начал выполнение.
		release := make(chan struct{})             // Барьер удерживает шаг до явного разрешения теста.
		stage := func(name string) execution.Job { // Обработчик выполняет действие тестового шага.
			return job(name, func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
				entered <- name
				<-release
				return nil
			}, nil)
		}
		group := parallel(stage("one"), parallel(stage("two"), stage("three"))) // Собираем тестовую параллельную группу.
		after := false                                                          // Часть ответа справа от курсора.
		p := pipeline(t, group, job("after", func(context.Context) error {      // Тестовый шаг имитирует нужный вариант выполнения.
			after = true
			return nil
		}, nil))
		result := make(chan error, 1) // Буфер одного результата не задерживает завершение отправителя.
		go func() {                   // В горутине выполняем проверяемое действие.
			result <- p.Execute(t.Context()) // Запускаем pipeline с тестовым контекстом.
		}()
		synctest.Wait()                 // Ждём блокировки фоновых горутин перед проверкой состояния.
		if len(entered) != 3 || after { // Проверяем количество доступных элементов.
			t.Fatalf("children are not concurrent: started=%d, after=%v", len(entered), after) // Завершаем тест с диагностикой полученных значений.
		}
		if snapshot := group.Snapshot(); snapshot.Status != execution.Running || snapshot.Jobs[1].Jobs[1].Stage.Status != execution.Running { // Читаем состояние параллельной группы.
			t.Fatalf("nested live snapshot: %+v", snapshot) // Завершаем тест с диагностикой полученных значений.
		}
		close(release)                   // Снимаем тестовый барьер завершения шага.
		if err := <-result; err != nil { // Ошибка текущей операции.
			t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
		}
		if !after || p.Snapshot().Status != execution.Succeeded { // Читаем снимок состояния pipeline.
			t.Fatal("pipeline did not finish after its children") // Немедленно завершаем тест при нарушении условия.
		}
	})
}

// TestParallelFailureWaitsForSiblingAndSkipsDownstream проверяет ожидание соседней ветви и пропуск последующих задач при сбое.
func TestParallelFailureWaitsForSiblingAndSkipsDownstream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // Проверяем конкурентный код с управляемым временем.
		failure := errors.New("failed child") // Имитируем ошибку одной параллельной ветви.
		release := make(chan struct{})        // Барьер удерживает шаг до явного разрешения теста.
		group := parallel(                    // Собираем тестовую параллельную группу.
			job("fails", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
				return failure
			}, nil),
			job("finishes", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
				<-release
				return nil
			}, nil),
		)
		after := job("must not run", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			t.Error("downstream ran") // Фиксируем недопустимое действие проверяемого кода.
			return nil
		}, nil)
		p := pipeline(t, group, after) // Собираем pipeline из тестовых задач.
		result := make(chan error, 1)  // Буфер одного результата не задерживает завершение отправителя.
		go func() {                    // В горутине выполняем проверяемое действие.
			result <- p.Execute(t.Context()) // Запускаем pipeline с тестовым контекстом.
		}()
		synctest.Wait() // Ждём блокировки фоновых горутин перед проверкой состояния.
		select {
		case err := <-result: // Ошибка текущей операции.
			t.Fatalf("pipeline abandoned its active sibling: %v", err) // Завершаем тест с диагностикой полученных значений.
		default:
		}
		close(release)                                 // Снимаем тестовый барьер завершения шага.
		if err := <-result; !errors.Is(err, failure) { // Проверяем причину с учётом обёрток ошибки.
			t.Fatalf("original error lost: %v", err) // Завершаем тест с диагностикой полученных значений.
		}
		snapshot := p.Snapshot() // Читаем снимок состояния pipeline.
		if snapshot.Status != execution.Failed || snapshot.Jobs[0].Jobs[1].Status != execution.Succeeded || snapshot.Jobs[1].Stage.Status != execution.Skipped {
			t.Fatalf("unexpected failure snapshot: %+v", snapshot) // Завершаем тест с диагностикой полученных значений.
		}
	})
}

// TestFallbackRecoveryAndErrors проверяет успешное восстановление и объединение ошибок при его сбое.
func TestFallbackRecoveryAndErrors(t *testing.T) {
	primaryErr, fallbackErr := errors.New("primary failed"), errors.New("fallback failed") // Раздельные ошибки исходного шага и запасного пути.
	for _, tc := range []struct {
		name       string
		primary    error
		fallback   error
		wantJob    execution.Status
		wantStage  execution.Status
		wantBackup execution.Status
	}{
		{"unused", nil, nil, execution.Succeeded, execution.Succeeded, execution.Skipped},
		{"recovers", primaryErr, nil, execution.Succeeded, execution.Failed, execution.Succeeded},
		{"both fail", primaryErr, fallbackErr, execution.Failed, execution.Failed, execution.Failed},
	} {
		t.Run(tc.name, func(t *testing.T) { // Проверяем отдельный вариант входных данных.
			backup := job("backup", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
				return tc.fallback
			}, nil)
			primary := job("primary", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
				return tc.primary
			}, backup)
			p := pipeline(t, primary)     // Собираем pipeline из тестовых задач.
			err := p.Execute(t.Context()) // Запускаем pipeline с тестовым контекстом.
			if tc.wantJob == execution.Failed {
				if !errors.Is(err, primaryErr) || !errors.Is(err, fallbackErr) { // Проверяем причину с учётом обёрток ошибки.
					t.Fatalf("fallback lost an error: %v", err) // Завершаем тест с диагностикой полученных значений.
				}
			} else if err != nil {
				t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
			}
			snapshot := p.Snapshot().Jobs[0] // Читаем снимок состояния pipeline.
			if snapshot.Status != tc.wantJob || snapshot.Stage.Status != tc.wantStage || snapshot.Fallback.Status != tc.wantBackup {
				t.Fatalf("fallback snapshot: %+v", snapshot) // Завершаем тест с диагностикой полученных значений.
			}
			if tc.primary != nil && !strings.Contains(snapshot.Stage.Error, primaryErr.Error()) { // Проверяем наличие ожидаемого фрагмента текста.
				t.Fatal("original stage failure is missing from the snapshot") // Немедленно завершаем тест при нарушении условия.
			}
		})
	}
}

// TestPanicIsReportedAndCanRecover проверяет сохранение паники и запуск запасной задачи.
func TestPanicIsReportedAndCanRecover(t *testing.T) {
	backup := job("backup", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
		return nil
	}, nil)
	primary := job("panics", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
		panic("broken step") // Имитируем аварийный сбой шага.
	}, backup)
	p := pipeline(t, primary)                      // Собираем pipeline из тестовых задач.
	if err := p.Execute(t.Context()); err != nil { // Запускаем pipeline с тестовым контекстом.
		t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
	}
	snapshot := p.Snapshot().Jobs[0]                                                                                                                   // Читаем снимок состояния pipeline.
	if snapshot.Status != execution.Succeeded || snapshot.Stage.Status != execution.Failed || !strings.Contains(snapshot.Stage.Error, "broken step") { // Проверяем наличие ожидаемого фрагмента текста.
		t.Fatalf("panic was not recorded: %+v", snapshot) // Завершаем тест с диагностикой полученных значений.
	}
}

// TestParallelFallbackRunsAfterEveryChildFinishes проверяет запуск восстановления только после всех дочерних задач.
func TestParallelFallbackRunsAfterEveryChildFinishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // Проверяем конкурентный код с управляемым временем.
		release := make(chan struct{})                                // Барьер удерживает шаг до явного разрешения теста.
		backup := job("group recovery", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			return nil
		}, nil)
		group := execution.NewParallelJobs([]execution.Job{ // Объединяем тестовые задачи для параллельного запуска.
			job("fails", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
				return errors.New("failed child") // Имитируем ошибку одной параллельной ветви.
			}, nil),
			job("finishes", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
				<-release
				return nil
			}, nil),
		}, backup)
		after := false                                                     // Часть ответа справа от курсора.
		p := pipeline(t, group, job("after", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			after = true
			return nil
		}, nil))
		result := make(chan error, 1) // Буфер одного результата не задерживает завершение отправителя.
		go func() {                   // В горутине выполняем проверяемое действие.
			result <- p.Execute(t.Context()) // Запускаем pipeline с тестовым контекстом.
		}()
		synctest.Wait()                                    // Ждём блокировки фоновых горутин перед проверкой состояния.
		if backup.Snapshot().Status != execution.Pending { // Читаем состояние запасной задачи.
			t.Fatal("fallback started while a child was still running") // Немедленно завершаем тест при нарушении условия.
		}
		close(release)                   // Снимаем тестовый барьер завершения шага.
		if err := <-result; err != nil { // Ошибка текущей операции.
			t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
		}
		snapshot := p.Snapshot().Jobs[0] // Читаем снимок состояния pipeline.
		if !after || snapshot.Status != execution.Succeeded || snapshot.Jobs[0].Status != execution.Failed || snapshot.Fallback.Status != execution.Succeeded {
			t.Fatalf("group recovery lost its children's status: %+v", snapshot) // Завершаем тест с диагностикой полученных значений.
		}
	})
}

// TestCancellationWaitsForChildrenAndSkipsFallback проверяет ожидание задач и пропуск восстановления при отмене.
func TestCancellationWaitsForChildrenAndSkipsFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // Проверяем конкурентный код с управляемым временем.
		ctx, cancel := context.WithCancel(t.Context())        // Создаём контекст с отдельной функцией отмены.
		defer cancel()                                        // Отменяем дочерний контекст и освобождаем ресурсы.
		release := make(chan struct{})                        // Барьер удерживает шаг до явного разрешения теста.
		backup := job("backup", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			t.Error("fallback ran on cancellation") // Фиксируем недопустимое действие проверяемого кода.
			return nil
		}, nil)
		cooperative := job("cooperative", func(ctx context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			<-ctx.Done()     // Ожидаем отмены контекста.
			return ctx.Err() // Проверяем отмену или истечение срока контекста.
		}, backup)
		blocking := job("blocking", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			<-release
			return nil
		}, nil)
		group := parallel(cooperative, blocking)            // Собираем тестовую параллельную группу.
		after := job("after", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			t.Error("downstream ran on cancellation") // Фиксируем недопустимое действие проверяемого кода.
			return nil
		}, nil)
		p := pipeline(t, group, after) // Собираем pipeline из тестовых задач.
		result := make(chan error, 1)  // Буфер одного результата не задерживает завершение отправителя.
		go func() {                    // В горутине выполняем проверяемое действие.
			result <- p.Execute(ctx) // Запускаем pipeline с тестовым контекстом.
		}()
		synctest.Wait() // Ждём блокировки фоновых горутин перед проверкой состояния.
		cancel()        // Отменяем дочерний контекст и освобождаем ресурсы.
		synctest.Wait() // Ждём блокировки фоновых горутин перед проверкой состояния.
		select {
		case err := <-result: // Ошибка текущей операции.
			t.Fatalf("pipeline left an active stage behind: %v", err) // Завершаем тест с диагностикой полученных значений.
		default:
		}
		close(release)                                          // Снимаем тестовый барьер завершения шага.
		if err := <-result; !errors.Is(err, context.Canceled) { // Проверяем причину с учётом обёрток ошибки.
			t.Fatalf("cancellation error: %v", err) // Завершаем тест с диагностикой полученных значений.
		}
		snapshot := p.Snapshot()                                                                                                                                                                     // Читаем снимок состояния pipeline.
		if snapshot.Status != execution.Canceled || snapshot.Jobs[0].Status != execution.Canceled || snapshot.Jobs[1].Status != execution.Skipped || backup.Snapshot().Status != execution.Skipped { // Читаем состояние запасной задачи.
			t.Fatalf("cancellation snapshot: %+v", snapshot) // Завершаем тест с диагностикой полученных значений.
		}
	})
}

// TestPipelinePollsEverySecond проверяет период опроса и немедленный итоговый снимок.
func TestPipelinePollsEverySecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // Проверяем конкурентный код с управляемым временем.
		release := make(chan struct{})                            // Барьер удерживает шаг до явного разрешения теста.
		primary := job("long step", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			<-release
			return nil
		}, nil)
		p := pipeline(t, primary)     // Собираем pipeline из тестовых задач.
		result := make(chan error, 1) // Буфер одного результата не задерживает завершение отправителя.
		go func() {                   // В горутине выполняем проверяемое действие.
			result <- p.Execute(t.Context()) // Запускаем pipeline с тестовым контекстом.
		}()
		synctest.Wait()                                       // Ждём блокировки фоновых горутин перед проверкой состояния.
		if p.Snapshot().Jobs[0].Status != execution.Pending { // Читаем снимок состояния pipeline.
			t.Fatal("job snapshot updated before the first tick") // Немедленно завершаем тест при нарушении условия.
		}
		time.Sleep(time.Second - time.Nanosecond)             // Продвигаем тестовое время к нужной границе опроса.
		synctest.Wait()                                       // Ждём блокировки фоновых горутин перед проверкой состояния.
		if p.Snapshot().Jobs[0].Status != execution.Pending { // Читаем снимок состояния pipeline.
			t.Fatal("job snapshot updated before one second") // Немедленно завершаем тест при нарушении условия.
		}
		time.Sleep(time.Nanosecond)                                                                                               // Продвигаем тестовое время к нужной границе опроса.
		synctest.Wait()                                                                                                           // Ждём блокировки фоновых горутин перед проверкой состояния.
		if snapshot := p.Snapshot(); snapshot.Status != execution.Running || snapshot.Jobs[0].Stage.Status != execution.Running { // Читаем снимок состояния pipeline.
			t.Fatalf("missing one-second poll: %+v", snapshot) // Завершаем тест с диагностикой полученных значений.
		}
		close(release)                   // Снимаем тестовый барьер завершения шага.
		if err := <-result; err != nil { // Ошибка текущей операции.
			t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
		}
		if p.Snapshot().Status != execution.Succeeded { // Читаем снимок состояния pipeline.
			t.Fatal("final snapshot waits for another tick") // Немедленно завершаем тест при нарушении условия.
		}
	})
}

// TestSnapshotsAreDetached проверяет независимость выданных снимков от внутреннего состояния.
func TestSnapshotsAreDetached(t *testing.T) {
	backup := job("backup", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
		return nil
	}, nil)
	primary := job("original", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
		return nil
	}, backup)
	p := pipeline(t, parallel(primary)) // Собираем pipeline из тестовых задач.
	snapshot := p.Snapshot()            // Читаем снимок состояния pipeline.
	snapshot.Jobs[0].Status = execution.Failed
	snapshot.Jobs[0].Jobs[0].Stage.Name = "mutated"
	snapshot.Jobs[0].Jobs[0].Fallback.Stage.Name = "mutated fallback"
	current := p.Snapshot() // Читаем снимок состояния pipeline.
	if current.Jobs[0].Status != execution.Pending || current.Jobs[0].Jobs[0].Stage.Name != "original" || current.Jobs[0].Jobs[0].Fallback.Stage.Name != "backup" {
		t.Fatal("snapshot exposes internal mutable state") // Немедленно завершаем тест при нарушении условия.
	}
	if err := p.Execute(t.Context()); err != nil { // Запускаем pipeline с тестовым контекстом.
		t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
	}
	if snapshot.Status != execution.Pending {
		t.Fatal("an old snapshot changed after execution") // Немедленно завершаем тест при нарушении условия.
	}
}

// TestConcurrentRunAndSnapshot проверяет запрет повторного запуска и конкурентное чтение снимков.
func TestConcurrentRunAndSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // Проверяем конкурентный код с управляемым временем.
		release := make(chan struct{})                           // Барьер удерживает шаг до явного разрешения теста.
		primary := job("blocking", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			<-release
			return nil
		}, nil)
		p := pipeline(t, primary)     // Собираем pipeline из тестовых задач.
		result := make(chan error, 1) // Буфер одного результата не задерживает завершение отправителя.
		go func() {                   // В горутине выполняем проверяемое действие.
			result <- p.Execute(t.Context()) // Запускаем pipeline с тестовым контекстом.
		}()
		synctest.Wait()                                                                  // Ждём блокировки фоновых горутин перед проверкой состояния.
		if err := p.Execute(t.Context()); !errors.Is(err, execution.ErrAlreadyStarted) { // Запускаем pipeline с тестовым контекстом.
			t.Fatalf("concurrent run: %v", err) // Завершаем тест с диагностикой полученных значений.
		}
		var readers sync.WaitGroup // Ожидание всех конкурентных читателей состояния.
		for range 8 {
			readers.Go(func() { // В горутине выполняем проверяемое действие.
				for range 100 {
					_ = p.Snapshot()       // Читаем снимок состояния pipeline.
					_ = primary.Snapshot() // Читаем состояние исходной задачи.
				}
			})
		}
		close(release)                   // Снимаем тестовый барьер завершения шага.
		readers.Wait()                   // Дожидаемся всех читателей снимков.
		if err := <-result; err != nil { // Ошибка текущей операции.
			t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
		}
	})
}

// TestStageFlagsControlPipeline проверяет критичность шага и выбор пользователя для необязательного шага.
func TestStageFlagsControlPipeline(t *testing.T) {
	failure := errors.New("stage failed") // Ошибка шага для проверки обработки сбоев.
	var ran []string                      // Порядок фактически запущенных шагов.
	record := func(name string, err error) execution.Stage {
		return execution.NewStage(name, "", func(context.Context) error {
			ran = append(ran, name)
			return err
		})
	}

	bestEffort := pipeline(t, // Сбой best-effort шага не останавливает следующие.
		execution.NewSingleJob(record("best-effort", failure).BestEffort(), nil),
		execution.NewSingleJob(record("next", nil), nil),
	)
	if err := bestEffort.Execute(t.Context()); err != nil {
		t.Fatalf("best-effort failure stopped the pipeline: %v", err)
	}
	if snapshot := bestEffort.Snapshot(); len(snapshot.Warnings) != 1 || snapshot.Jobs[0].Status != execution.Failed || snapshot.Jobs[1].Status != execution.Succeeded {
		t.Fatalf("best-effort snapshot: %+v", snapshot)
	}

	critical := pipeline(t, // Сбой критичного шага пропускает остальные.
		execution.NewSingleJob(record("critical", failure), nil),
		execution.NewSingleJob(record("skipped", nil), nil),
	)
	if err := critical.Execute(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("critical failure was ignored: %v", err)
	}
	if snapshot := critical.Snapshot(); snapshot.Jobs[1].Status != execution.Skipped || len(snapshot.Warnings) != 0 {
		t.Fatalf("critical snapshot: %+v", snapshot)
	}

	ran = nil // Необязательный шаг по отказу пользователя не запускается.
	optional := pipeline(t, execution.NewSingleJob(record("optional", nil).Optional().BestEffort(), nil))
	ctx := execution.WithOptionalDecision(t.Context(), func(context.Context, execution.Stage) (bool, error) {
		return false, nil
	})
	if err := optional.Execute(ctx); err != nil {
		t.Fatalf("declined optional step: %v", err)
	}
	if len(ran) != 0 || optional.Snapshot().Jobs[0].Status != execution.Skipped {
		t.Fatalf("optional step ran after a refusal: %v", ran)
	}
}

// TestValidationAndOwnership проверяет дерево задач, исключительное владение и откат неудачной сборки.
func TestValidationAndOwnership(t *testing.T) {
	noop := func(context.Context) error { // Пустое успешное действие для проверки структуры дерева.
		return nil
	}
	var nilSingle *execution.SingleJob      // Типизированный nil одиночной задачи для проверки валидации.
	var nilParallel *execution.ParallelJobs // Типизированный nil группы для проверки валидации.
	shared := job("shared", noop, nil)      // Создаём тестовую задачу с заданным действием.
	for _, jobs := range [][]execution.Job{
		{nil}, {nilSingle}, {nilParallel},
		{job("", noop, nil)}, {job("missing executive", nil, nil)}, // Создаём тестовую задачу с заданным действием.
		{parallel()}, {parallel(nil)}, // Собираем тестовую параллельную группу.
		{shared, shared},
		{parallel(shared), shared},             // Собираем тестовую параллельную группу.
		{job("primary", noop, shared), shared}, // Создаём тестовую задачу с заданным действием.
	} {
		if _, err := execution.NewPipeline(jobs...); err == nil { // Проверяем и собираем тестовое дерево задач.
			t.Fatalf("accepted invalid tree: %v", jobs) // Завершаем тест с диагностикой полученных значений.
		}
	}
	first := pipeline(t, shared)                                      // Собираем pipeline из тестовых задач.
	unowned := job("unowned", noop, nil)                              // Создаём тестовую задачу с заданным действием.
	if _, err := execution.NewPipeline(unowned, shared); err == nil { // Проверяем и собираем тестовое дерево задач.
		t.Fatal("accepted a job owned by another pipeline") // Немедленно завершаем тест при нарушении условия.
	}

	second := pipeline(t, unowned) // Собираем pipeline из тестовых задач.
	for _, p := range []*execution.Pipeline{first, second} {
		if err := p.Execute(t.Context()); err != nil { // Запускаем pipeline с тестовым контекстом.
			t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
		}
	}
}

// TestCanceledBeforeStartAndEmptyPipeline проверяет отмену до запуска и пустой pipeline.
func TestCanceledBeforeStartAndEmptyPipeline(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())              // Создаём контекст с отдельной функцией отмены.
	cancel()                                                    // Отменяем дочерний контекст и освобождаем ресурсы.
	primary := job("not started", func(context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
		t.Error("canceled stage ran") // Фиксируем недопустимое действие проверяемого кода.
		return nil
	}, nil)
	p := pipeline(t, primary)                                     // Собираем pipeline из тестовых задач.
	if err := p.Execute(ctx); !errors.Is(err, context.Canceled) { // Запускаем pipeline с тестовым контекстом.
		t.Fatalf("canceled run: %v", err) // Завершаем тест с диагностикой полученных значений.
	}
	if p.Snapshot().Status != execution.Canceled || primary.Snapshot().Stage.Status != execution.Skipped { // Читаем снимок состояния pipeline.
		t.Fatal("unstarted work was not skipped") // Немедленно завершаем тест при нарушении условия.
	}
	empty := pipeline(t)                                                                                 // Собираем pipeline из тестовых задач.
	if err := empty.Execute(t.Context()); err != nil || empty.Snapshot().Status != execution.Succeeded { // Запускаем pipeline без задач.
		t.Fatalf("empty pipeline: %v", err) // Завершаем тест с диагностикой полученных значений.
	}
}
