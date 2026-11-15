package ui

import (
	"fmt"
	"strings"

	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// span хранит фрагмент строки графа с одним стилем.
type span struct {
	text  string // Текст одного фрагмента.
	style color  // Стиль этого фрагмента.
}

// graphLine объединяет фрагменты строки и признак выполняемого шага.
type graphLine struct {
	parts   []span // Последовательно рисуемые фрагменты строки.
	running bool   // Признак строки с выполняемым шагом для автопрокрутки.
}

// statusLabel выбирает короткую подпись и цвет состояния.
func statusLabel(status execution.Status) (string, color) {
	switch status {
	case execution.Running:
		return "> RUN", yellow
	case execution.Succeeded:
		return "+ OK", green
	case execution.Failed:
		return "! FAIL", red
	case execution.Canceled:
		return "x STOP", yellow
	case execution.Skipped:
		return "- SKIP", muted
	default:
		return ". WAIT", muted
	}
}

// short сокращает текст по ширине ячеек, добавляя многоточие.
func short(text string, columns int) string {
	if textWidth(text) <= columns { // Текст уже помещается без сокращения.
		return text
	}
	lines := wrap(text, max(1, columns-1)) // Оставляем одну ячейку под многоточие.
	if len(lines) == 0 {                   // Проверяем количество доступных элементов.
		return ""
	}
	return lines[0] + "…"
}

// nodeLabel формирует подпись узла, включая отметку успешного восстановления.
func nodeLabel(job execution.JobSnapshot, columns int) span {
	label, style := statusLabel(job.Status) // Подбираем подпись и цвет статуса.
	name := "parallel"                      // Подпись узла без отдельного шага.
	if job.Stage != nil {
		name = job.Stage.Name
	}
	if job.Stage != nil && job.Stage.Status == execution.Failed && job.Status == execution.Succeeded {
		label = "+ RECOVERED"
	}
	return span{text: "[" + label + " " + short(name, max(4, columns-textWidth(label)-3)) + "]", style: style} // Обрезаем подпись по доступной ширине.
}

// graph строит строки последовательных задач и вложенных параллельных или запасных ветвей.
func graph(jobs []execution.JobSnapshot, columns int) []graphLine {
	var lines []graphLine // Строки подготовленного текста.
	var row graphLine     // Накопленная строка последовательных узлов.
	used := 0             // Количество уже занятых ячеек строки.
	flush := func() {     // Сохраняем строку и сбрасываем накопленные фрагменты.
		if len(row.parts) > 0 { // Проверяем количество доступных элементов.
			lines = append(lines, row) // Сохраняем готовую строку в результате.
			row = graphLine{}
			used = 0
		}
	}
	var branch func(execution.JobSnapshot, string)            // Рекурсивный обработчик вложенных ветвей графа.
	branch = func(job execution.JobSnapshot, prefix string) { // Рекурсивно рисуем узел, дочерние и запасную ветви.
		node := nodeLabel(job, max(6, columns-textWidth(prefix)))                                                                                               // Собираем подпись узла графа.
		lines = append(lines, graphLine{parts: []span{{text: prefix, style: muted}, node}, running: job.Stage != nil && job.Stage.Status == execution.Running}) // Сохраняем готовую строку в результате.
		for i, child := range job.Jobs {
			connector := "├─ "                               // Символ связи с дочерней ветвью.
			if i+1 == len(job.Jobs) && job.Fallback == nil { // Последняя ветвь без fallback получает конечный соединитель.
				connector = "└─ "
			}
			branch(child, prefix+connector) // Добавляем вложенную ветвь графа.
		}
		if job.Fallback != nil {
			branch(*job.Fallback, prefix+"└─ fallback → ") // Добавляем вложенную ветвь графа.
		}
	}
	for _, job := range jobs {
		if len(job.Jobs) > 0 || job.Fallback != nil { // Проверяем количество доступных элементов.
			flush()         // Переносим накопленную строку графа в результат.
			branch(job, "") // Добавляем вложенную ветвь графа.
			continue
		}
		node := nodeLabel(job, min(columns, 20)) // Собираем подпись узла графа.
		needed := textWidth(node.text)           // Считаем ширину текста в ячейках терминала.
		if used > 0 && used+4+needed > columns {
			flush()                                                       // Переносим накопленную строку графа в результат.
			row.parts = append(row.parts, span{text: "↳ ", style: muted}) // Добавляем фрагмент в текущую строку графа.
			used = 2
		}
		if used > 2 {
			row.parts = append(row.parts, span{text: " → ", style: muted}) // Добавляем фрагмент в текущую строку графа.
			used += 3
		}
		row.parts = append(row.parts, node) // Добавляем фрагмент в текущую строку графа.
		used += needed
		row.running = row.running || job.Status == execution.Running
	}
	flush() // Переносим накопленную строку графа в результат.
	return lines
}

// taskView содержит данные одной видимой панели задачи.
type taskView struct {
	id, name, description string           // Идентификатор и описание шага для панели.
	status                execution.Status // Состояние исходного шага.
	err                   string           // Текст ошибки исходного шага.
}

// tasks выбирает задачи для панелей; после завершения отдаёт всё место результату и путям бэкапов.
func (m *model) tasks() []taskView {

	if m.phase == finishedPhase {
		status := execution.Succeeded // Итоговое состояние отображаемой задачи.
		if m.err != nil {
			status = execution.Failed
			if execution.IsCancellation(m.err) { // Отличаем чистую отмену от реального сбоя.
				status = execution.Canceled
			}
		}
		return []taskView{{id: "result", name: m.entries[m.selection].Name, description: m.entries[m.selection].Description, status: status}}
	}
	var all, active []taskView               // Все шаги и отдельно выполняемые шаги.
	var walk func(execution.JobSnapshot)     // Рекурсивный обход шагов и запасных ветвей.
	walk = func(job execution.JobSnapshot) { // Собираем панели из шагов дерева задач.
		if job.Stage != nil {
			stage := job.Stage                                                                                                     // Описание исходного шага задачи.
			view := taskView{id: job.ID, name: stage.Name, description: stage.Description, status: stage.Status, err: stage.Error} // Данные панели текущего шага.
			all = append(all, view)                                                                                                // Сохраняем данные шага среди всех задач.
			if stage.Status == execution.Running {
				active = append(active, view) // Добавляем задачу к видимым активным панелям.
			}
		}
		for _, child := range job.Jobs {
			walk(child) // Обходим дочерний узел и его запасную ветвь.
		}
		if job.Fallback != nil {
			walk(*job.Fallback) // Обходим дочерний узел и его запасную ветвь.
		}
	}
	for _, job := range m.snapshot.Jobs {
		walk(job) // Обходим дочерний узел и его запасную ветвь.
	}
	for _, dialog := range m.dialogs {
		task := dialog.request.task // Метаданные задачи, ожидающей ответа.
		found := false              // Признак уже добавленной активной задачи.
		for _, view := range active {
			found = found || view.id == task.ID
		}
		if !found {
			active = append(active, taskView{id: task.ID, name: task.Name, description: task.Description, status: execution.Running}) // Добавляем задачу к видимым активным панелям.
		}
	}
	if len(active) > 0 { // Проверяем количество доступных элементов.
		return active
	}
	if len(all) == 0 { // Проверяем количество доступных элементов.
		status := execution.Running // Итоговое состояние отображаемой задачи.
		if m.phase == finishedPhase {
			status = execution.Succeeded
			if m.err != nil {
				status = execution.Failed
				if execution.IsCancellation(m.err) { // Отличаем чистую отмену от реального сбоя.
					status = execution.Canceled
				}
			}
		}
		return []taskView{{id: "setup", name: "Подготовка сценария", description: m.outcome(), status: status}} // Получаем понятное пользователю состояние сценария.
	}
	if m.snapshot.Status == execution.Failed || m.snapshot.Status == execution.Canceled {
		for _, task := range all {
			if task.status == execution.Failed || task.status == execution.Canceled {
				active = append(active, task) // Добавляем задачу к видимым активным панелям.
			}
		}
		if len(active) > 0 { // Проверяем количество доступных элементов.
			return active
		}
	}
	if m.phase == runningPhase {
		for _, view := range all {
			if view.status == execution.Pending {
				return []taskView{view}
			}
		}
	}
	return all[max(0, len(all)-4):] // Оставляем последние четыре задачи для показа.
}

// taskRects делит всю область между одной–четырьмя задачами.
// Четыре панели образуют сетку 2×2; в узком терминале панели идут строками.
func taskRects(area rect, count int) []rect {
	count = min(count, 4) // Ограничиваем число панелей четырьмя.
	if count < 1 {
		return nil
	}
	columns, rows := 1, count // Число колонок и строк сетки панелей.
	if count <= 3 && area.w >= count*28 {
		columns, rows = count, 1
	}
	if count == 4 && area.w >= 56 {
		columns, rows = 2, 2
	}
	result := make([]rect, 0, count) // Выделяем буфер нужной длины.
	for i := 0; i < count; i++ {     // Рассчитываем область каждой видимой панели.
		column, row := i%columns, i/columns                                      // Положение панели внутри сетки.
		x0, x1 := area.x+column*area.w/columns, area.x+(column+1)*area.w/columns // Левая и правая границы панели.
		y0, y1 := area.y+row*area.h/rows, area.y+(row+1)*area.h/rows             // Верхняя и нижняя границы панели.
		result = append(result, rect{x: x0, y: y0, w: x1 - x0, h: y1 - y0})      // Добавляем рассчитанную область панели.
	}
	return result
}

// taskTitle объединяет статус и имя задачи в заголовок панели.
func taskTitle(task taskView) string {
	label, _ := statusLabel(task.status)            // Подбираем подпись и цвет статуса.
	return fmt.Sprintf("%s · %s", label, task.name) // Форматируем текст для отображения.
}

// safeText ограничивает объём текста для отрисовки, сохраняя исходную ошибку для возврата из UI.
func safeText(text string) string {

	if len(text) > 8192 { // Проверяем количество доступных элементов.
		text = text[:8192] + "\n…"
	}
	return strings.TrimSpace(text) // Убираем пробелы по краям ограниченного текста.
}
