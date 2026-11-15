package ui

import (
	"context"
	"strings"

	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// phase задаёт текущий экран: выбор, подтверждение, выполнение или результат.
type phase uint8

const (
	choosePhase   phase = iota // Выбор сценария в каталоге.
	confirmPhase               // Подтверждение выбранного сценария.
	runningPhase               // Подготовка или выполнение сценария.
	finishedPhase              // Показ результата завершившегося сценария.
)

// action описывает действие, которое событийный цикл должен выполнить после нажатия клавиши.
type action uint8

const (
	noAction    action = iota // Событийный цикл продолжает обработку.
	startAction               // Нужно запустить выбранный сценарий.
	stopAction                // Нужно отменить выполнение и дождаться остановки.
	quitAction                // Нужно выйти после завершения начатой работы.
	playAction                // Нужно запустить звук текущего запроса.
)

// dialog хранит запрос, редактируемый ответ, курсор и состояние диалога.
type dialog struct {
	request *request // Запрос и задача, ожидающая ответа.
	text    []rune   // Ответ в символах Unicode; очищается при закрытии.
	cursor  int      // Позиция курсора в символах.
	yes     bool     // Выбор подтверждения; по умолчанию отказ.
	playing bool     // Признак уже запущенного проигрывателя.
	scroll  int      // Прокрутка длинного текста вопроса.
}

// model хранит состояние UI; изменяется только внутри одного событийного цикла.
type model struct {
	entries                []Entry                    // Каталог доступных сценариев.
	selection              int                        // Индекс выбранного сценария.
	phase                  phase                      // Текущий экран приложения.
	yes                    bool                       // Выбор подтверждения запуска.
	width, height          int                        // Доступный размер кадра.
	generation             int                        // Номер запуска для отсеивания старых событий.
	run                    *Run                       // Собранный сценарий текущего запуска.
	snapshot               execution.PipelineSnapshot // Последний полученный снимок pipeline.
	err                    error                      // Итоговая ошибка сценария.
	stopping, exitAfterRun bool                       // Ожидание остановки и выход после завершения.
	dialogs                []*dialog                  // Ожидающие ответа вопросы задач.
	focus                  int                        // Индекс диалога в фокусе.
	taskPage               int                        // Страница панелей, по четыре задачи.
	graphOffset            int                        // Ручная прокрутка графа по строкам.
	followGraph            bool                       // Автопрокрутка к выполняемому шагу.
	messages               []string                   // Последние сообщения журнала приложения.
}

// newModel создаёт экран выбора со включённым слежением за текущим шагом.
func newModel(entries []Entry) *model {
	return &model{entries: entries, followGraph: true} // Начинаем с выбора сценария и слежения за текущим шагом.
}

// poll обновляет локальный снимок pipeline, если сценарий уже собран.
func (m *model) poll() {
	if m.run != nil {
		m.snapshot = m.run.Pipeline.Snapshot() // Обновляем локальный снимок выполняемого pipeline.
	}
}

// add создаёт диалог либо сразу отменяет запрос, который уже нельзя показать.
func (m *model) add(r *request) {
	if r.ctx.Err() != nil || m.phase != runningPhase || m.stopping { // Отменённый запрос не должен открывать диалог.
		clear(r.wav)                            // Затираем WAV после использования или отмены.
		reply(r, answer{err: context.Canceled}) // Отправляем результат ожидающей задаче без блокировки.
		return
	}
	d := &dialog{request: r, text: []rune(cleanInput(r.question.Default))} // Создаём диалог с очищенным начальным ответом.
	if len(d.text) > 4096 {                                                // Ограничиваем редактируемый ответ 4096 символами.
		d.text = d.text[:4096]
	}
	d.cursor = len(d.text)           // Ставим курсор в конец ответа.
	m.dialogs = append(m.dialogs, d) // Ставим новый вопрос в очередь диалогов.
}

// reply отправляет единственный ответ, не блокируя событийный цикл.
func reply(r *request, result answer) {
	select {
	case r.reply <- result:
	default:
	}
}

// resolve очищает буферы, удаляет диалог и возвращает ответ нужной задаче.
// Завершение чужого вопроса сохраняет фокус текущего ввода.
func (m *model) resolve(r *request, result answer) {
	for i, d := range m.dialogs {
		if d.request == r {
			clear(d.text) // Затираем старый ответ, включая секретные данные.
			if !d.playing {
				clear(r.wav) // Затираем WAV после использования или отмены.
			}
			m.dialogs = append(m.dialogs[:i], m.dialogs[i+1:]...) // Удаляем завершённый диалог из списка.

			if i < m.focus {
				m.focus--
			}
			m.focus = min(m.focus, max(0, len(m.dialogs)-1)) // Сохраняем фокус в списке после удаления чужого диалога.
			break
		}
	}
	reply(r, result) // Отправляем результат ожидающей задаче без блокировки.
}

// prune удаляет диалоги отменённых запросов и останавливаемого сценария.
func (m *model) prune() {
	for i := 0; i < len(m.dialogs); { // Проверяем количество доступных элементов.
		d := m.dialogs[i]                             // Берём очередной диалог для проверки отмены.
		if d.request.ctx.Err() != nil || m.stopping { // Удаляем вопрос после отмены его задачи.
			m.resolve(d.request, answer{err: context.Canceled}) // Закрываем диалог и возвращаем ответ его задаче.
		} else {
			i++
		}
	}
}

// currentDialog возвращает диалог в фокусе либо nil.
func (m *model) currentDialog() *dialog {
	if len(m.dialogs) == 0 { // Проверяем количество доступных элементов.
		return nil
	}
	return m.dialogs[m.focus%len(m.dialogs)] // Учитываем длину текущего буфера или списка.
}

// finish сохраняет результат сценария и закрывает оставшиеся вопросы.
func (m *model) finish(result runResult) {
	m.run, m.err, m.phase = result.run, result.err, finishedPhase
	m.stopping = false
	m.poll()                 // Читаем свежий снимок pipeline.
	for len(m.dialogs) > 0 { // Проверяем количество доступных элементов.
		m.resolve(m.dialogs[0].request, answer{err: context.Canceled}) // Закрываем диалог и возвращаем ответ его задаче.
	}
}

// log добавляет сообщение и оставляет только сто последних записей.
func (m *model) log(message string) {
	m.messages = append(m.messages, strings.TrimSpace(message)) // Добавляем очищенную запись в журнал UI.
	if len(m.messages) > 100 {                                  // Проверяем количество доступных элементов.
		m.messages = m.messages[len(m.messages)-100:] // Учитываем длину текущего буфера или списка.
	}
}

// begin сбрасывает состояние предыдущего запуска и запрашивает новый.
func (m *model) begin() action {
	m.phase = runningPhase
	m.generation++
	m.run = nil
	m.err = nil
	m.stopping = false
	m.exitAfterRun = false
	m.snapshot = execution.PipelineSnapshot{Status: execution.Pending}
	m.messages = nil
	m.taskPage = 0
	m.graphOffset = 0
	m.followGraph = true
	return startAction
}

// handleKey меняет состояние экрана и возвращает действие для событийного цикла.
func (m *model) handleKey(k key) action {
	if k.name == "interrupt" {
		return quitAction
	}
	if k.name == "escape" {
		switch m.phase {
		case confirmPhase:
			m.phase = choosePhase
		case runningPhase:
			m.stopping = true
			return stopAction
		default:
			return quitAction
		}
		return noAction
	}

	if m.width < 50 || m.height < 16 { // В тесном окне подтверждение не видно: нажатие игнорируется.
		return noAction
	}
	text := strings.ToLower(k.text) // Нормализуем буквы клавиши для команд UI.
	switch m.phase {
	case choosePhase:
		switch {
		case k.name == "up" || text == "k":
			m.selection = (m.selection + len(m.entries) - 1) % len(m.entries) // Циклически переводим выбор к соседнему сценарию.
		case k.name == "down" || text == "j":
			m.selection = (m.selection + 1) % len(m.entries) // Циклически переводим выбор к соседнему сценарию.
		case k.name == "enter":
			m.phase = confirmPhase
			m.yes = false
		case text == "q":
			return quitAction
		}
	case confirmPhase:
		switch {
		case k.name == "left":
			m.yes = false
		case k.name == "right":
			m.yes = true
		case k.name == "tab":
			m.yes = !m.yes
		case text == "y" || text == "д":
			return m.begin() // Подготавливаем модель к новому запуску.
		case text == "n" || text == "н":
			m.phase = choosePhase
		case k.name == "enter":
			if m.yes {
				return m.begin() // Подготавливаем модель к новому запуску.
			}
			m.phase = choosePhase
		case text == "q":
			return quitAction
		}
	case runningPhase:
		if m.stopping {
			return noAction
		}
		if k.name == "tab" || k.name == "backtab" {
			if len(m.dialogs) > 1 { // Проверяем количество доступных элементов.
				step := 1 // Смещение фокуса на следующий или предыдущий вопрос.
				if k.name == "backtab" {
					step = len(m.dialogs) - 1 // Учитываем длину текущего буфера или списка.
				}
				m.focus = (m.focus + step) % len(m.dialogs) // Учитываем длину текущего буфера или списка.
			} else {
				m.taskPage++
			}
			return noAction
		}
		if d := m.currentDialog(); d != nil { // Находим вопрос, которому принадлежит фокус ввода.
			return m.edit(d, k) // Передаём клавишу редактору текущего ответа.
		}
		switch {
		case k.name == "pageup" || k.name == "up":
			m.followGraph = false
			m.graphOffset = max(0, m.graphOffset-3) // Ограничиваем прокрутку доступными строками.
		case k.name == "pagedown" || k.name == "down":
			m.followGraph = false
			m.graphOffset += 3
		case k.name == "home":
			m.followGraph = true
		case text == "q":
			return quitAction
		}
	case finishedPhase:
		if k.name == "enter" {
			m.phase = choosePhase
			m.run = nil
			m.err = nil
		}
		if text == "q" {
			return quitAction
		}
		if k.name == "pageup" {
			m.followGraph = false
			m.graphOffset = max(0, m.graphOffset-3) // Ограничиваем прокрутку доступными строками.
		}
		if k.name == "pagedown" {
			m.followGraph = false
			m.graphOffset += 3
		}
	}
	return noAction
}

// edit обрабатывает ввод, подтверждение и запуск звука в текущем диалоге.
func (m *model) edit(d *dialog, k key) action {
	r := d.request // Запрос, которому принадлежит текущий диалог.
	if k.name == "pageup" {
		d.scroll = max(0, d.scroll-3) // Ограничиваем размер доступной областью.
		return noAction
	}
	if k.name == "pagedown" {
		d.scroll += 3
		return noAction
	}
	if r.kind == playRequest {
		if k.name == "enter" && !d.playing {
			return playAction
		}
		return noAction
	}
	if r.kind == confirmRequest {
		switch {
		case k.name == "left":
			d.yes = false
		case k.name == "right":
			d.yes = true
		case k.name == "text" && (k.text == "y" || k.text == "Y" || k.text == "д" || k.text == "Д"):
			m.resolve(r, answer{value: "yes"}) // Закрываем диалог и возвращаем ответ его задаче.
		case k.name == "text" && (k.text == "n" || k.text == "N" || k.text == "н" || k.text == "Н"):
			m.resolve(r, answer{value: "no"}) // Закрываем диалог и возвращаем ответ его задаче.
		case k.name == "enter":
			value := "no" // Ответ подтверждения; по умолчанию отказ.
			if d.yes {
				value = "yes"
			}
			m.resolve(r, answer{value: value}) // Закрываем диалог и возвращаем ответ его задаче.
		}
		return noAction
	}
	switch k.name {
	case "text", "paste":
		text := []rune(cleanInput(k.text))                // Очищаем ввод и разбираем его на Unicode-символы.
		text = text[:min(len(text), 4096-len(d.text))]    // Вставка не должна превысить лимит длины ответа.
		updated := make([]rune, 0, len(d.text)+len(text)) // Новый буфер вмещает исходный ответ и вставку.
		updated = append(updated, d.text[:d.cursor]...)   // Собираем новый буфер ответа вокруг позиции курсора.
		updated = append(updated, text...)                // Собираем новый буфер ответа вокруг позиции курсора.
		updated = append(updated, d.text[d.cursor:]...)   // Собираем новый буфер ответа вокруг позиции курсора.
		clear(d.text)                                     // Затираем старый ответ, включая секретные данные.
		d.text = updated
		d.cursor += len(text) // Ставим курсор в конец ответа.
	case "left":
		d.cursor = max(0, d.cursor-1) // Оставляем курсор внутри редактируемого ответа.
	case "right":
		d.cursor = min(len(d.text), d.cursor+1) // Оставляем курсор внутри редактируемого ответа.
	case "home":
		d.cursor = 0
	case "end":
		d.cursor = len(d.text) // Ставим курсор в конец ответа.
	case "backspace":
		if d.cursor > 0 {
			copy(d.text[d.cursor-1:], d.text[d.cursor:]) // Сдвигаем символы ответа поверх удаляемого символа.
			d.text[len(d.text)-1] = 0                    // Учитываем длину текущего буфера или списка.
			d.text = d.text[:len(d.text)-1]              // Учитываем длину текущего буфера или списка.
			d.cursor--
		}
	case "delete":
		if d.cursor < len(d.text) { // Проверяем количество доступных элементов.
			copy(d.text[d.cursor:], d.text[d.cursor+1:]) // Сдвигаем символы ответа поверх удаляемого символа.
			d.text[len(d.text)-1] = 0                    // Учитываем длину текущего буфера или списка.
			d.text = d.text[:len(d.text)-1]              // Учитываем длину текущего буфера или списка.
		}
	case "clear":
		clear(d.text) // Затираем старый ответ, включая секретные данные.
		d.text = nil
		d.cursor = 0
	case "enter":
		m.resolve(r, answer{value: string(d.text)}) // Закрываем диалог и возвращаем ответ его задаче.
	}
	return noAction
}

// outcome формирует заголовок текущего состояния сценария.
func (m *model) outcome() string {
	if m.stopping && m.phase == runningPhase {
		return "Ожидание безопасной остановки"
	}
	if m.phase == finishedPhase {
		if execution.IsCancellation(m.err) { // Отличаем чистую отмену от реального сбоя.
			return "Сценарий отменён"
		}
		if m.err != nil {
			return "Сценарий завершился с ошибкой"
		}
		return "Сценарий завершён"
	}
	if m.run == nil {
		return "Сборка сценария"
	}
	return "Выполнение сценария"
}
