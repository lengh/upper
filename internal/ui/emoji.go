package ui

import "strings"

// emojiList is a compact table of the most used Discord shortcodes, ordered
// by popularity so completion offers the likely one first. Aliases share
// an emoji. Typing :name: in a message converts it on send, as the official
// client does; Tab completes.
var emojiList = func() []struct{ name, emoji string } {
	const table = `
thumbsup,+1 👍|joy 😂|heart ❤️|sob 😭|skull 💀|fire 🔥|pray 🙏|eyes 👀|rofl 🤣|smile 😄
thinking 🤔|white_check_mark ✅|100 💯|tada 🎉|clap 👏|wave 👋|ok_hand 👌|slight_smile 🙂|grin 😁|sweat_smile 😅
smiling_face_with_3_hearts 🥰|heart_eyes 😍|blush 😊|wink 😉|sunglasses 😎|thumbsdown,-1 👎|x ❌|rocket 🚀|sparkles ✨|star ⭐
upside_down 🙃|neutral_face 😐|expressionless 😑|rolling_eyes 🙄|unamused 😒|pensive 😔|cry 😢|angry 😠|rage 😡|scream 😱
flushed 😳|pleading_face 🥺|zany_face 🤪|nerd 🤓|partying_face 🥳|yawning_face 🥱|sleeping 😴|sweat 😓|confused 😕|grimacing 😬
smirk 😏|relieved 😌|innocent 😇|kissing_heart 😘|stuck_out_tongue 😛|stuck_out_tongue_winking_eye 😜|money_mouth 🤑|hugging 🤗|shushing_face 🤫|face_with_hand_over_mouth 🤭
lying_face 🤥|mask 😷|nauseated_face 🤢|vomiting_face 🤮|hot_face 🥵|cold_face 🥶|exploding_head 🤯|cowboy 🤠|clown 🤡|ghost 👻
alien 👽|robot 🤖|poop 💩|smiling_imp 😈|see_no_evil 🙈|hear_no_evil 🙉|speak_no_evil 🙊|muscle 💪|point_up ☝️|point_right 👉
point_left 👈|point_down 👇|raised_hands 🙌|handshake 🤝|v ✌️|fingers_crossed 🤞|metal 🤘|call_me 🤙|writing_hand ✍️|facepalm 🤦
shrug 🤷|man_shrugging 🤷‍♂️|woman_shrugging 🤷‍♀️|brain 🧠|broken_heart 💔|orange_heart 🧡|yellow_heart 💛|green_heart 💚|blue_heart 💙|purple_heart 💜
black_heart 🖤|white_heart 🤍|two_hearts 💕|sparkling_heart 💖|heartpulse 💗|boom 💥|zap ⚡|sunny ☀️|rainbow 🌈|snowflake ❄️
coffee ☕|beer 🍺|beers 🍻|pizza 🍕|cake 🍰|cookie 🍪|popcorn 🍿|taco 🌮|apple 🍎|eggplant 🍆
peach 🍑|cat 🐱|dog 🐶|fox 🦊|frog 🐸|monkey 🐒|unicorn 🦄|penguin 🐧|snake 🐍|crab 🦀
bug 🐛|butterfly 🦋|rose 🌹|sunflower 🌻|seedling 🌱|evergreen_tree 🌲|earth_americas 🌎|moon 🌙|cloud ☁️|umbrella ☔
trophy 🏆|medal 🏅|soccer ⚽|basketball 🏀|video_game 🎮|game_die 🎲|musical_note 🎵|notes 🎶|headphones 🎧|microphone 🎤
computer 💻|keyboard ⌨️|desktop 🖥️|iphone 📱|bulb 💡|wrench 🔧|hammer 🔨|gear ⚙️|lock 🔒|unlock 🔓
key 🔑|link 🔗|pushpin 📌|paperclip 📎|memo 📝|book 📖|calendar 📅|chart_with_upwards_trend 📈|moneybag 💰|gift 🎁
bell 🔔|loudspeaker 📢|mag 🔍|hourglass ⌛|alarm_clock ⏰|warning ⚠️|no_entry ⛔|question ❓|exclamation ❗|bangbang ‼️
heavy_check_mark ✔️|heavy_plus_sign ➕|heavy_minus_sign ➖|arrow_up ⬆️|arrow_down ⬇️|arrow_right ➡️|arrow_left ⬅️|repeat 🔁|recycle ♻️|infinity ♾️
red_circle 🔴|green_circle 🟢|yellow_circle 🟡|blue_circle 🔵|white_circle ⚪|black_circle ⚫|crown 👑|gem 💎|ring 💍|zzz 💤
sweat_drops 💦|dash 💨|speech_balloon 💬|thought_balloon 💭|wave_hello 👋|salute 🫡|melting_face 🫠|face_holding_back_tears 🥹|saluting_face 🫡|smiling_face_with_tear 🥲`
	var out []struct{ name, emoji string }
	for _, row := range strings.Split(strings.TrimSpace(table), "\n") {
		for _, item := range strings.Split(row, "|") {
			names, emoji, _ := strings.Cut(strings.TrimSpace(item), " ")
			for _, n := range strings.Split(names, ",") {
				out = append(out, struct{ name, emoji string }{n, emoji})
			}
		}
	}
	return out
}()

var emojiByName = func() map[string]string {
	m := make(map[string]string, len(emojiList))
	for _, e := range emojiList {
		if _, ok := m[e.name]; !ok {
			m[e.name] = e.emoji
		}
	}
	return m
}()
