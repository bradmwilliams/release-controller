package main

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog"

	imagev1 "github.com/openshift/api/image/v1"
	releasecontroller "github.com/openshift/release-controller/pkg/release-controller"
)

func (c *Controller) ensureTagPointsToRelease(release *releasecontroller.Release, to, from string) error {
	if to == from {
		return nil
	}
	fromTag := releasecontroller.FindTagReference(release.Target, from)
	toTag := releasecontroller.FindTagReference(release.Target, to)
	if fromTag == nil {
		// tag was deleted
		return nil
	}
	if toTag != nil {
		if toTag.From != nil && toTag.From.Kind == "ImageStreamTag" && toTag.From.Name == from && toTag.From.Namespace == "" {
			// already set to the correct location
			return nil
		}
	}
	target := release.Target.DeepCopy()
	toTag = releasecontroller.FindTagReference(target, to)
	if toTag == nil {
		target.Spec.Tags = append(target.Spec.Tags, imagev1.TagReference{
			Name: to,
		})
		toTag = &target.Spec.Tags[len(target.Spec.Tags)-1]
	}
	toTag.From = &corev1.ObjectReference{Kind: "ImageStreamTag", Name: from}
	toTag.ImportPolicy = imagev1.TagImportPolicy{}

	is, err := c.imageClient.ImageStreams(target.Namespace).Update(context.TODO(), target, metav1.UpdateOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	klog.V(2).Infof("Updated image stream tag %s/%s:%s to point to %s", release.Target.Namespace, release.Target.Name, to, from)
	updateReleaseTarget(release, is)
	return nil
}

func (c *Controller) ensureImageStreamMatchesRelease(release *releasecontroller.Release, toNamespace, toName, from string, tags, excludeTags []string) error {
	if len(tags) == 0 {
		klog.V(4).Infof("Ensure image stream %s/%s has contents of %s", toNamespace, toName, from)
	} else {
		klog.V(4).Infof("Ensure image stream %s/%s has tags from %s: %s", toNamespace, toName, from, strings.Join(tags, ", "))
	}
	if toNamespace == release.Source.Namespace && toName == release.Source.Name {
		return nil
	}
	fromTag := releasecontroller.FindTagReference(release.Target, from)
	if fromTag == nil {
		// tag was deleted
		return nil
	}

	mirror, err := releasecontroller.GetMirror(release, from, c.releaseLister)
	if err != nil {
		klog.V(2).Infof("Error getting release mirror image stream: %v", err)
		return nil
	}

	lister := c.publishLister.ImageStreams(toNamespace)
	if lister == nil {
		return fmt.Errorf("cannot publish to namespace %s, namespace was not registered for either release or publish", toNamespace)
	}
	target, err := lister.Get(toName)
	if errors.IsNotFound(err) {
		// TODO: create it?
		klog.V(2).Infof("Target image stream doesn't exist yet: %v", err)
		return nil
	}
	if err != nil {
		// TODO
		klog.V(2).Infof("Error getting publish image stream: %v", err)
		return nil
	}

	if len(tags) == 0 {
		set := fmt.Sprintf("release.openshift.io/source-%s", release.Config.Name)
		if value, ok := target.Annotations[set]; ok && value == from {
			klog.V(2).Infof("Published image stream %s/%s is up to date", toNamespace, toName)
			return nil
		}

		excluded := sets.NewString(excludeTags...)
		processed := sets.NewString()
		finalRefs := make([]imagev1.TagReference, 0, len(mirror.Spec.Tags))
		for _, tag := range mirror.Spec.Tags {
			if processed.Has(tag.Name) || excluded.Has(tag.Name) {
				continue
			}
			processed.Insert(tag.Name)
			finalRefs = append(finalRefs, tag)
		}
		for _, tag := range target.Spec.Tags {
			if processed.Has(tag.Name) {
				continue
			}
			finalRefs = append(finalRefs, tag)
		}
		sort.Slice(finalRefs, func(i, j int) bool {
			return finalRefs[i].Name < finalRefs[j].Name
		})

		target = target.DeepCopy()
		target.Spec.Tags = finalRefs
		if target.Annotations == nil {
			target.Annotations = make(map[string]string)
		}
		target.Annotations[set] = from

	} else {
		var copied *imagev1.ImageStream
		processed := sets.NewString(excludeTags...)
		for _, tag := range tags {
			if processed.Has(tag) {
				continue
			}
			processed.Insert(tag)

			sourceTag := releasecontroller.FindTagReference(mirror, tag)
			if sourceTag == nil {
				klog.Warningf("The tag %s should be mirrored from %s to %s, but is not in the source tags", tag, release.Config.Name, toName)
				continue
			}
			targetTag := releasecontroller.FindTagReference(target, tag)
			if targetTag != nil && reflect.DeepEqual(targetTag.From, sourceTag.From) {
				// tag is identical
				continue
			}
			if copied == nil {
				copied = target.DeepCopy()
			}
			if targetTag == nil {
				copied.Spec.Tags = append(copied.Spec.Tags, *sourceTag)
			} else {
				targetTag = releasecontroller.FindTagReference(copied, tag)
				*targetTag = *sourceTag
			}
		}
		if copied == nil {
			return nil
		}
		target = copied
	}

	_, err = c.imageClient.ImageStreams(target.Namespace).Update(context.TODO(), target, metav1.UpdateOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		klog.V(2).Infof("Updated image stream %s/%s to point to contents of %s", toNamespace, toName, from)
	} else {
		klog.V(2).Infof("Updated image stream %s/%s with tags from %s: %s", toNamespace, toName, from, strings.Join(tags, ", "))
	}
	return nil
}
