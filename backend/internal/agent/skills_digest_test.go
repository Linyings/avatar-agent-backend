package agent

import (
	"strings"
	"testing"
)

func TestSkillsDigestIdentifiesEveryEnabledSkillVersion(t *testing.T) {
	got := SkillsDigest()
	if !strings.HasPrefix(got, "talking-head-storyboard@") || !strings.Contains(got, ",portrait-realism@") {
		t.Fatalf("unexpected digest %q", got)
	}
	if SkillsDigest() != got {
		t.Fatal("digest must be stable")
	}
	original := skillDigestFiles
	defer func() { skillDigestFiles = original }()
	skillDigestFiles = func(name string) ([][]byte, error) {
		files, err := original(name)
		return append(files, []byte("changed")), err
	}
	if SkillsDigest() == got {
		t.Fatal("changing any skill resource must change the digest")
	}
}

func TestSkillRequiresFillingAvatarWindowsWithoutLetterboxing(t *testing.T) {
	doc, err := ReadSkill("talking-head-storyboard", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc.Content, "裁切或留边") || !strings.Contains(doc.Content, "禁止留边") || !strings.Contains(doc.Content, "腰部以上中景") {
		t.Fatal("skill must require face-cropped windows and medium-shot source footage")
	}
}
