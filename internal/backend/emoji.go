package backend

type EmojiOption struct {
	Value string
	Name  string
}

func EmojiCatalog() []EmojiOption {
	return []EmojiOption{
		{"😀", "grinning face happy"}, {"😃", "smiling face happy"},
		{"😄", "smile happy laugh"}, {"😁", "beaming face grin"},
		{"😂", "tears of joy laugh"}, {"🤣", "rolling laughing"},
		{"😊", "smiling blush"}, {"🙂", "slightly smiling"},
		{"😉", "wink"}, {"😍", "heart eyes love"},
		{"🥰", "smiling hearts love"}, {"😘", "kiss"},
		{"😋", "delicious yum"}, {"😎", "sunglasses cool"},
		{"🤔", "thinking"}, {"🤗", "hug"},
		{"🤭", "hand over mouth"}, {"😐", "neutral face"},
		{"🙄", "rolling eyes"}, {"😴", "sleeping"},
		{"😢", "cry sad"}, {"😭", "loud crying sad"},
		{"😡", "angry"}, {"🤯", "mind blown shocked"},
		{"🥳", "party celebration"}, {"😇", "angel innocent"},
		{"🤡", "clown"}, {"💀", "skull dead"},
		{"👍", "thumbs up yes like"}, {"👎", "thumbs down no dislike"},
		{"👏", "clap applause"}, {"🙌", "raised hands celebrate"},
		{"🙏", "folded hands please thanks prayer"}, {"🤝", "handshake"},
		{"👋", "wave hello goodbye"}, {"👌", "ok hand"},
		{"✌️", "victory peace"}, {"🤞", "fingers crossed luck"},
		{"💪", "strong muscle"}, {"❤️", "red heart love"},
		{"🧡", "orange heart"}, {"💛", "yellow heart"},
		{"💚", "green heart"}, {"💙", "blue heart"},
		{"💜", "purple heart"}, {"🖤", "black heart"},
		{"💔", "broken heart"}, {"💕", "two hearts love"},
		{"🔥", "fire hot"}, {"✨", "sparkles"},
		{"🎉", "party popper celebration"}, {"🎂", "birthday cake"},
		{"🎁", "gift present"}, {"🌟", "star glowing"},
		{"✅", "check mark done yes"}, {"❌", "cross mark no"},
		{"⚠️", "warning"}, {"💯", "hundred perfect"},
		{"🚀", "rocket launch"}, {"☕", "coffee"},
		{"🍻", "beer cheers"}, {"🌹", "rose flower"},
		{"🐶", "dog"}, {"🐱", "cat"},
	}
}

func ReactionCatalog() []EmojiOption {
	return []EmojiOption{
		{"❤", "heart love"}, {"👍", "thumbs up yes like"},
		{"👎", "thumbs down no dislike"}, {"🔥", "fire hot"},
		{"🥰", "smiling hearts love"}, {"👏", "clap applause"},
		{"😁", "beaming face grin"}, {"🤔", "thinking"},
		{"🤯", "mind blown shocked"}, {"😢", "cry sad"},
		{"🎉", "party celebration"}, {"🙏", "folded hands please thanks prayer"},
		{"👌", "ok hand"}, {"🤡", "clown"},
		{"💯", "hundred perfect"}, {"🤣", "rolling laughing"},
		{"💔", "broken heart"}, {"😐", "neutral face"},
		{"😭", "loud crying sad"}, {"😴", "sleeping"},
		{"😍", "heart eyes love"}, {"🤝", "handshake"},
		{"😇", "angel innocent"}, {"😡", "angry"},
	}
}
