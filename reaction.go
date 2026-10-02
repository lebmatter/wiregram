package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/gotd/td/tg"
)

func reactionLabel(reaction tg.ReactionClass) string {
	switch value := reaction.(type) {
	case *tg.ReactionEmoji:
		return value.Emoticon
	case *tg.ReactionCustomEmoji:
		return "◇"
	default:
		return ""
	}
}

func sameReaction(a, b tg.ReactionClass) bool {
	switch left := a.(type) {
	case *tg.ReactionEmoji:
		right, ok := b.(*tg.ReactionEmoji)
		return ok && left.Emoticon == right.Emoticon
	case *tg.ReactionCustomEmoji:
		right, ok := b.(*tg.ReactionCustomEmoji)
		return ok && left.DocumentID == right.DocumentID
	default:
		return false
	}
}

func chosenReactions(message *tg.Message, selected tg.ReactionClass) ([]tg.ReactionClass, bool) {
	type choice struct {
		order    int
		reaction tg.ReactionClass
	}
	var chosen []choice
	alreadyChosen := false
	if reactions, ok := message.GetReactions(); ok {
		for _, count := range reactions.Results {
			order, ok := count.GetChosenOrder()
			if !ok {
				continue
			}
			if sameReaction(count.Reaction, selected) {
				alreadyChosen = true
				continue
			}
			chosen = append(chosen, choice{order, count.Reaction})
		}
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].order < chosen[j].order })
	result := make([]tg.ReactionClass, 0, len(chosen)+1)
	for _, item := range chosen {
		result = append(result, item.reaction)
	}
	if !alreadyChosen {
		result = append(result, selected)
	}
	return result, alreadyChosen
}

func optimisticReactions(message *tg.Message, selected tg.ReactionClass, remove bool) tg.MessageReactions {
	current, _ := message.GetReactions()
	current.Results = append([]tg.ReactionCount(nil), current.Results...)
	maxOrder := -1
	found := false
	results := current.Results[:0]
	for _, count := range current.Results {
		if order, ok := count.GetChosenOrder(); ok && order > maxOrder {
			maxOrder = order
		}
		if !sameReaction(count.Reaction, selected) {
			results = append(results, count)
			continue
		}
		found = true
		if remove {
			count.Count--
			count.Flags.Unset(0)
			count.ChosenOrder = 0
			if count.Count > 0 {
				results = append(results, count)
			}
			continue
		}
		count.Count++
		count.SetChosenOrder(maxOrder + 1)
		results = append(results, count)
	}
	if !found && !remove {
		count := tg.ReactionCount{Reaction: selected, Count: 1}
		count.SetChosenOrder(maxOrder + 1)
		results = append(results, count)
	}
	current.Results = results
	return current
}

func reactionUpdateFromResult(result tg.UpdatesClass, entry chat, messageID int) (tg.MessageReactions, bool) {
	var updates []tg.UpdateClass
	switch value := result.(type) {
	case *tg.Updates:
		updates = value.Updates
	case *tg.UpdatesCombined:
		updates = value.Updates
	case *tg.UpdateShort:
		updates = []tg.UpdateClass{value.Update}
	}
	for _, value := range updates {
		if update, ok := value.(*tg.UpdateMessageReactions); ok && update.MsgID == messageID && keyOf(update.Peer) == entry.key {
			return update.Reactions, true
		}
	}
	return tg.MessageReactions{}, false
}

func (c *client) sendReaction(ctx context.Context, entry chat, message *tg.Message, emoticon string) (tg.MessageReactions, error) {
	if message == nil || message.ID <= 0 {
		return tg.MessageReactions{}, fmt.Errorf("select a sent message before reacting")
	}
	if current, ok := message.GetReactions(); ok && current.Min {
		result, err := c.api.MessagesGetMessagesReactions(ctx, &tg.MessagesGetMessagesReactionsRequest{
			Peer: entry.peer,
			ID:   []int{message.ID},
		})
		if err != nil {
			return tg.MessageReactions{}, fmt.Errorf("load current reactions: %w", err)
		}
		if reactions, ok := reactionUpdateFromResult(result, entry, message.ID); ok {
			copy := *message
			copy.SetReactions(reactions)
			message = &copy
		}
	}
	selected := &tg.ReactionEmoji{Emoticon: emoticon}
	reactions, remove := chosenReactions(message, selected)
	result, err := c.api.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{
		AddToRecent: true,
		Peer:        entry.peer,
		MsgID:       message.ID,
		Reaction:    reactions,
	})
	if err != nil {
		return tg.MessageReactions{}, fmt.Errorf("send reaction: %w", err)
	}
	if update, ok := reactionUpdateFromResult(result, entry, message.ID); ok {
		return update, nil
	}
	return optimisticReactions(message, selected, remove), nil
}
