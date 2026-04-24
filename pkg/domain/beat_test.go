package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBeatWithMembers_PrimaryTopic(t *testing.T) {
	mkItem := func(topics []string) ClassifiedItem {
		return ClassifiedItem{
			Item: &Item{},
			Classification: &Classification{
				Topics: topics,
			},
		}
	}

	tests := []struct {
		name string
		beat BeatWithMembers
		want string
	}{
		{
			name: "single member single topic",
			beat: BeatWithMembers{Members: []ClassifiedItem{mkItem([]string{"ai"})}},
			want: "ai",
		},
		{
			name: "multiple members, majority topic wins",
			beat: BeatWithMembers{Members: []ClassifiedItem{
				mkItem([]string{"ai", "tech"}),
				mkItem([]string{"ai", "policy"}),
				mkItem([]string{"tech"}),
			}},
			want: "ai",
		},
		{
			name: "tie broken by first occurrence",
			beat: BeatWithMembers{Members: []ClassifiedItem{
				mkItem([]string{"tech", "ai"}),
				mkItem([]string{"ai", "tech"}),
			}},
			want: "tech",
		},
		{
			name: "no members",
			beat: BeatWithMembers{},
			want: "",
		},
		{
			name: "members with no topics",
			beat: BeatWithMembers{Members: []ClassifiedItem{
				mkItem(nil),
				mkItem([]string{}),
			}},
			want: "",
		},
		{
			name: "member with nil classification",
			beat: BeatWithMembers{Members: []ClassifiedItem{
				{Item: &Item{}},
			}},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.beat.PrimaryTopic())
		})
	}
}
