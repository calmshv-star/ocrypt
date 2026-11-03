package notifications

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Message is one rendered notification: an email subject and a plain-text
// body. Telegram receives the subject and body as one plain-text message, so
// nothing in it is ever interpreted as markup.
type Message struct {
	Subject string
	Text    string
}

// Telegram returns the message as one chat text.
func (m Message) Telegram() string { return m.Subject + "\n\n" + m.Text }

type payload struct {
	Merchant      string `json:"merchant"`
	OrderID       string `json:"order_id"`
	AmountMinor   string `json:"amount_minor"`
	Currency      string `json:"currency"`
	CurrencyScale *int   `json:"currency_scale"`
	Livemode      *bool  `json:"livemode"`
	EndpointHost  string `json:"endpoint_host"`
	EventType     string `json:"event_type"`
	HTTPStatus    int    `json:"http_status"`
	Device        string `json:"device"`
	SignedInAt    string `json:"signed_in_at"`
}

// cabinetPath is where each event is handled in the cabinet.
var cabinetPath = map[string]string{"paid": "/#/intents", "underpaid": "/#/intents", "review": "/#/intents", "webhook_failed": "/#/webhooks", "sign_in": "/#/security"}

// Render builds the message of a job in its locale (English when unknown).
// Every value from the payload is reduced to printable text first.
func Render(event, locale string, raw json.RawMessage, cabinetURL string) (Message, error) {
	words, ok := catalog[locale]
	if !ok {
		words = catalog["en"]
	}
	path, ok := cabinetPath[event]
	if !ok {
		return Message{}, ErrInvalid
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Message{}, ErrInvalid
	}
	values := map[string]string{
		"merchant": plain(p.Merchant, 200, "Ocrypt"),
		"order":    plain(p.OrderID, 128, "—"),
		"currency": plain(p.Currency, 16, ""),
		"host":     plain(p.EndpointHost, 253, "—"),
		"event":    plain(p.EventType, 64, "—"),
		"url":      strings.TrimRight(cabinetURL, "/") + path,
	}
	scale := 0
	if p.CurrencyScale != nil {
		scale = *p.CurrencyScale
	}
	values["amount"] = decimal(p.AmountMinor, scale)
	values["status"] = ""
	if p.HTTPStatus >= 100 && p.HTTPStatus <= 599 {
		values["status"] = fill(words["webhook_failed.status"], map[string]string{"code": fmt.Sprint(p.HTTPStatus)})
	}
	values["device"] = deviceText(p.Device, words["unknown_device"])
	values["time"] = "—"
	if at, err := time.Parse(time.RFC3339Nano, p.SignedInAt); err == nil {
		values["time"] = at.UTC().Format("2006-01-02 15:04")
	}
	subject, body := words[event+".subject"], words[event+".body"]
	if subject == "" || body == "" {
		return Message{}, ErrInvalid
	}
	message := Message{Subject: fill(subject, values), Text: fill(body, values) + "\n\n" + fill(words["open"], values)}
	if p.Livemode != nil && !*p.Livemode {
		message.Subject = words["test"] + message.Subject
	}
	return message, nil
}

// TelegramReply is the bot's answer to a link attempt.
func TelegramReply(locale string, linked bool) string {
	words, ok := catalog[locale]
	if !ok {
		words = catalog["en"]
	}
	if linked {
		return words["telegram.linked"]
	}
	return words["telegram.invalid"]
}

func fill(template string, values map[string]string) string {
	pairs := make([]string, 0, len(values)*2)
	for key, value := range values {
		pairs = append(pairs, "{"+key+"}", value)
	}
	return strings.NewReplacer(pairs...).Replace(template)
}

// plain keeps printable characters only and bounds the length in runes.
func plain(value string, limit int, fallback string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, strings.TrimSpace(value))
	if runes := []rune(value); len(runes) > limit {
		value = string(runes[:limit])
	}
	if value = strings.TrimSpace(value); value == "" {
		return fallback
	}
	return value
}

// decimal writes an integer minor amount with the currency scale, without
// trailing fractional zeros; anything that is not a plain integer is "—".
func decimal(minor string, scale int) string {
	if minor == "" || len(minor) > 80 || scale < 0 || scale > 18 || strings.Trim(minor, "0123456789") != "" {
		return "—"
	}
	minor = strings.TrimLeft(minor, "0")
	if len(minor) <= scale {
		minor = strings.Repeat("0", scale-len(minor)+1) + minor
	}
	whole, fraction := minor[:len(minor)-scale], strings.TrimRight(minor[len(minor)-scale:], "0")
	if whole == "" {
		whole = "0"
	}
	if fraction == "" {
		return whole
	}
	return whole + "." + fraction
}

func deviceText(label, unknown string) string {
	browser, system, ok := strings.Cut(label, "/")
	if !ok || browser == "" || len(label) > 33 || strings.Trim(label, "/ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") != "" {
		return unknown
	}
	if system == "" {
		return browser
	}
	return browser + " · " + system
}

var catalog = map[string]map[string]string{
	"en": {
		"paid.subject":           "Payment received: {amount} {currency}",
		"paid.body":              "{merchant}: order {order} is paid, {amount} {currency}.",
		"underpaid.subject":      "Underpaid order {order}",
		"underpaid.body":         "{merchant}: order {order} received less than the amount due. Decide in the cabinet whether to accept it or ask for the rest.",
		"review.subject":         "Payment needs review: order {order}",
		"review.body":            "{merchant}: a payment for order {order} needs a decision (late, overpaid or ambiguous). Review it in the cabinet.",
		"webhook_failed.subject": "Webhook delivery failed: {host}",
		"webhook_failed.body":    "{merchant}: Ocrypt could not deliver {event} to {host} after all retries{status}. Check the endpoint and replay the delivery in the cabinet.",
		"webhook_failed.status":  " (last HTTP status {code})",
		"sign_in.subject":        "New sign-in to Ocrypt",
		"sign_in.body":           "A new session started on {device} at {time} UTC. If this was not you, end it under Security in the cabinet and change your password and second factor at your sign-in provider.",
		"test":                   "[Test] ",
		"unknown_device":         "an unknown device",
		"open":                   "Open the cabinet: {url}",
		"telegram.linked":        "Ocrypt notifications are now sent to this chat. You can turn them off in the cabinet under Notifications.",
		"telegram.invalid":       "This link has expired or was already used. Create a new one in the cabinet under Notifications.",
	},
	"ru": {
		"paid.subject":           "Платёж получен: {amount} {currency}",
		"paid.body":              "{merchant}: заказ {order} оплачен, {amount} {currency}.",
		"underpaid.subject":      "Недоплата по заказу {order}",
		"underpaid.body":         "{merchant}: по заказу {order} пришло меньше нужной суммы. Решите в кабинете, принять платёж или запросить остаток.",
		"review.subject":         "Платёж требует разбора: заказ {order}",
		"review.body":            "{merchant}: платёж по заказу {order} требует решения (опоздание, переплата или неоднозначность). Разберите его в кабинете.",
		"webhook_failed.subject": "Вебхук не доставлен: {host}",
		"webhook_failed.body":    "{merchant}: Ocrypt не смог доставить {event} на {host} после всех повторов{status}. Проверьте адрес и повторите доставку в кабинете.",
		"webhook_failed.status":  " (последний HTTP-статус {code})",
		"sign_in.subject":        "Новый вход в Ocrypt",
		"sign_in.body":           "Начат новый сеанс: {device}, {time} UTC. Если это были не вы, завершите его в разделе «Безопасность» и смените пароль и второй фактор у провайдера входа.",
		"test":                   "[Тест] ",
		"unknown_device":         "неизвестное устройство",
		"open":                   "Открыть кабинет: {url}",
		"telegram.linked":        "Уведомления Ocrypt теперь приходят в этот чат. Отключить их можно в кабинете, раздел «Уведомления».",
		"telegram.invalid":       "Ссылка устарела или уже использована. Создайте новую в кабинете, раздел «Уведомления».",
	},
	"zh": {
		"paid.subject":           "已收到付款：{amount} {currency}",
		"paid.body":              "{merchant}：订单 {order} 已付款，{amount} {currency}。",
		"underpaid.subject":      "订单 {order} 付款不足",
		"underpaid.body":         "{merchant}：订单 {order} 收到的金额少于应付金额。请在后台决定接受该付款还是要求补足余款。",
		"review.subject":         "付款需要审核：订单 {order}",
		"review.body":            "{merchant}：订单 {order} 的一笔付款需要处理（逾期、多付或无法确定）。请在后台审核。",
		"webhook_failed.subject": "Webhook 投递失败：{host}",
		"webhook_failed.body":    "{merchant}：Ocrypt 在全部重试后仍无法将 {event} 投递到 {host}{status}。请检查端点并在后台重新投递。",
		"webhook_failed.status":  "（最后的 HTTP 状态码 {code}）",
		"sign_in.subject":        "Ocrypt 有新的登录",
		"sign_in.body":           "新会话于 {time} UTC 在{device}上开始。如果不是您本人，请在后台“安全”中结束该会话，并在登录提供方处更改密码和第二因素。",
		"test":                   "[测试] ",
		"unknown_device":         "未知设备",
		"open":                   "打开后台：{url}",
		"telegram.linked":        "Ocrypt 通知现在会发送到此聊天。您可以在后台“通知”中关闭它们。",
		"telegram.invalid":       "此链接已过期或已被使用。请在后台“通知”中创建新链接。",
	},
	"es": {
		"paid.subject":           "Pago recibido: {amount} {currency}",
		"paid.body":              "{merchant}: el pedido {order} está pagado, {amount} {currency}.",
		"underpaid.subject":      "Pago insuficiente del pedido {order}",
		"underpaid.body":         "{merchant}: el pedido {order} recibió menos del importe debido. Decida en el panel si lo acepta o pide el resto.",
		"review.subject":         "Pago pendiente de revisión: pedido {order}",
		"review.body":            "{merchant}: un pago del pedido {order} necesita una decisión (tardío, en exceso o ambiguo). Revíselo en el panel.",
		"webhook_failed.subject": "Fallo de entrega del webhook: {host}",
		"webhook_failed.body":    "{merchant}: Ocrypt no pudo entregar {event} a {host} tras todos los reintentos{status}. Compruebe el endpoint y vuelva a enviar la entrega desde el panel.",
		"webhook_failed.status":  " (último estado HTTP {code})",
		"sign_in.subject":        "Nuevo inicio de sesión en Ocrypt",
		"sign_in.body":           "Se inició una nueva sesión en {device} a las {time} UTC. Si no fue usted, ciérrela en Seguridad del panel y cambie su contraseña y segundo factor en su proveedor de acceso.",
		"test":                   "[Prueba] ",
		"unknown_device":         "un dispositivo desconocido",
		"open":                   "Abrir el panel: {url}",
		"telegram.linked":        "Las notificaciones de Ocrypt llegarán ahora a este chat. Puede desactivarlas en el panel, en Notificaciones.",
		"telegram.invalid":       "Este enlace ha caducado o ya se usó. Cree uno nuevo en el panel, en Notificaciones.",
	},
	"fr": {
		"paid.subject":           "Paiement reçu : {amount} {currency}",
		"paid.body":              "{merchant} : la commande {order} est payée, {amount} {currency}.",
		"underpaid.subject":      "Paiement insuffisant pour la commande {order}",
		"underpaid.body":         "{merchant} : la commande {order} a reçu moins que le montant dû. Décidez dans l'espace marchand de l'accepter ou de demander le reste.",
		"review.subject":         "Paiement à examiner : commande {order}",
		"review.body":            "{merchant} : un paiement de la commande {order} demande une décision (en retard, en trop ou ambigu). Examinez-le dans l'espace marchand.",
		"webhook_failed.subject": "Échec de livraison du webhook : {host}",
		"webhook_failed.body":    "{merchant} : Ocrypt n'a pas pu livrer {event} à {host} après toutes les tentatives{status}. Vérifiez le point de terminaison et relancez la livraison depuis l'espace marchand.",
		"webhook_failed.status":  " (dernier statut HTTP {code})",
		"sign_in.subject":        "Nouvelle connexion à Ocrypt",
		"sign_in.body":           "Une nouvelle session a commencé sur {device} à {time} UTC. Si ce n'était pas vous, fermez-la dans Sécurité et changez votre mot de passe et votre second facteur chez votre fournisseur de connexion.",
		"test":                   "[Test] ",
		"unknown_device":         "un appareil inconnu",
		"open":                   "Ouvrir l'espace marchand : {url}",
		"telegram.linked":        "Les notifications Ocrypt arrivent désormais dans ce chat. Vous pouvez les désactiver dans l'espace marchand, rubrique Notifications.",
		"telegram.invalid":       "Ce lien a expiré ou a déjà été utilisé. Créez-en un nouveau dans l'espace marchand, rubrique Notifications.",
	},
	"de": {
		"paid.subject":           "Zahlung erhalten: {amount} {currency}",
		"paid.body":              "{merchant}: Bestellung {order} ist bezahlt, {amount} {currency}.",
		"underpaid.subject":      "Unterzahlung bei Bestellung {order}",
		"underpaid.body":         "{merchant}: Für Bestellung {order} ist weniger als der fällige Betrag eingegangen. Entscheiden Sie im Händlerbereich, ob Sie die Zahlung annehmen oder den Rest anfordern.",
		"review.subject":         "Zahlung muss geprüft werden: Bestellung {order}",
		"review.body":            "{merchant}: Eine Zahlung für Bestellung {order} braucht eine Entscheidung (verspätet, überzahlt oder mehrdeutig). Prüfen Sie sie im Händlerbereich.",
		"webhook_failed.subject": "Webhook-Zustellung fehlgeschlagen: {host}",
		"webhook_failed.body":    "{merchant}: Ocrypt konnte {event} nach allen Wiederholungen nicht an {host} zustellen{status}. Prüfen Sie den Endpunkt und stellen Sie die Zustellung im Händlerbereich erneut zu.",
		"webhook_failed.status":  " (letzter HTTP-Status {code})",
		"sign_in.subject":        "Neue Anmeldung bei Ocrypt",
		"sign_in.body":           "Eine neue Sitzung wurde um {time} UTC auf {device} gestartet. Falls Sie das nicht waren, beenden Sie sie unter Sicherheit und ändern Sie Passwort und zweiten Faktor bei Ihrem Anmeldeanbieter.",
		"test":                   "[Test] ",
		"unknown_device":         "einem unbekannten Gerät",
		"open":                   "Händlerbereich öffnen: {url}",
		"telegram.linked":        "Ocrypt-Benachrichtigungen kommen jetzt in diesen Chat. Sie können sie im Händlerbereich unter Benachrichtigungen abschalten.",
		"telegram.invalid":       "Dieser Link ist abgelaufen oder wurde bereits verwendet. Erstellen Sie im Händlerbereich unter Benachrichtigungen einen neuen.",
	},
}
