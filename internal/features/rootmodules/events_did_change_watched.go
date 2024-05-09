// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package rootmodules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/creachadair/jrpc2"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	"github.com/hashicorp/terraform-ls/internal/features/modules/ast"
	varast "github.com/hashicorp/terraform-ls/internal/features/variables/ast"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/protocol"
	"github.com/hashicorp/terraform-ls/internal/terraform/datadir"
	"github.com/hashicorp/terraform-ls/internal/uri"
)

func (f *RootModulesFeature) didChangeWatched(ctx context.Context, fileURI string, changeType protocol.FileChangeType) (job.IDs, error) {

	var ids job.IDs

	rawURI := string(fileURI)

	/*

		each feature will listen for did_change_watched
		and process their individual job


		check if file is opened, if open returned
		check if there are open files for the directory of the file that changed
			if open files, start processing jobs
			if not open, do not start jobs
	*/
	docHandle := document.HandleFromURI(rawURI)
	isOpen, err := f.documentStore.IsDocumentOpen(docHandle)
	if err != nil {
		f.logger.Printf("error when checking open document (%q changed): %s", rawURI, err)
	}
	if isOpen {
		f.logger.Printf("document is open - ignoring event for %q", rawURI)
		return ids, err // continue
	}

	// data dir is rootmodules feature
	if modUri, ok := datadir.ModuleUriFromDataDir(rawURI); ok {
		return f.handleModuleUrifromDataDir(modUri, changeType)
	}

	if modUri, ok := datadir.ModuleUriFromPluginLockFile(rawURI); ok {
		return f.handleModuleUriFromPluginLockFile(ctx, changeType, modUri)
	}

	if modUri, ok := datadir.ModuleUriFromModuleLockFile(rawURI); ok {
		return f.handleModuleUriFromModuleLockFile(modUri, changeType)
	}

	rawPath, err := uri.PathFromURI(rawURI)
	if err != nil {
		f.logger.Printf("error parsing %q: %s", rawURI, err)
		return ids, err // continue
	}

	switch changeType {
	case protocol.Deleted:
		// We don't know whether file or dir is being deleted
		// 1st we just blindly try to look it up as a directory
		// TODO! check other stores as well

		// TODO: we don't index anymore...is this needed?
		// _, err = f.RootRecordByPath(rawPath)
		// if err == nil {
		// 	f.removeIndexedModule(ctx, rawURI)
		// 	return ids, err // continue
		// }

		// 2nd we try again assuming it is a file
		parentDir := filepath.Dir(rawPath)
		// TODO! check other stores as well

		// _, err = f.RootRecordByPath(parentDir)
		// if err != nil {
		// 	f.logger.Printf("error finding module (%q deleted): %s", parentDir, err)
		// 	return ids, err // continue
		// }

		// TODO: figure out whether indexer methods stay
		// and check the parent directory still exists
		// fi, err := os.Stat(parentDir)
		// if err != nil {
		// 	if os.IsNotExist(err) {
		// 		// TODO: we don't index anymore...is this needed?
		// 		// if not, we remove the indexed module
		// 		f.removeIndexedModule(ctx, rawURI)

		// 		return ids, err // continue
		// 	}
		// 	f.logger.Printf("error checking existence (%q deleted): %s", parentDir, err)
		// 	return ids, err // continue
		// }
		// if !fi.IsDir() {
		// 	f.logger.Printf("error: %q (deleted) is not a directory", parentDir)
		// 	return ids, err // continue
		// }

		// TODO: figure out whether indexer methods stay
		// if the parent directory exists, we just need to
		// reparse the module after a file was deleted from it
		f.eventbus.DocumentChanged(eventbus.DocumentChangedEvent{
			Context: ctx,
			Path:    parentDir,
		})

		// ids = append(ids, jobIds...)
	case protocol.Changed:
		// Check if document is open and skip running any jobs
		// as we already did so as part of textDocument/didChange
		// which clients should always send for *open* documents
		// even if they change outside of the IDE.
		docHandle := document.HandleFromURI(rawURI)
		isOpen, err := f.documentStore.IsDocumentOpen(docHandle)
		if err != nil {
			f.logger.Printf("error when checking open document (%q changed): %s", rawURI, err)
		}
		if isOpen {
			f.logger.Printf("document is open - ignoring event for %q", rawURI)
			return ids, err // continue
		}

		ph, err := modHandleFromRawOsPath(ctx, rawPath)
		if err != nil {
			if err == ErrorSkip {
				return ids, err // continue
			}
			f.logger.Printf("error (%q changed): %s", rawURI, err)
			return ids, err // continue
		}

		// // TODO! check other stores as well

		_, err = f.RootRecordByPath(ph.DirHandle.Path())
		if err != nil {
			f.logger.Printf("error finding module (%q changed): %s", rawURI, err)
			return ids, err // continue
		}

		parentDir := filepath.Dir(rawPath)
		f.eventbus.DocumentChanged(eventbus.DocumentChangedEvent{
			Context: ctx,
			Path:    parentDir,
		})

		// ids = append(ids, jobIds...)
	case protocol.Created:
		/*
			previously we determine if current file is open
			if open, we ignore event and stop processing the file
			if not open, we queue jobs to start indexing the module the file is in
		*/
		ph, err := modHandleFromRawOsPath(ctx, rawPath)
		if err != nil {
			if err == ErrorSkip {
				return ids, err // continue
			}
			f.logger.Printf("error (%q created): %s", rawURI, err)
			return ids, err // continue
		}

		if ph.IsDir {
			// TODO: what do we do with walker paths?
			// err = svc.stateStore.WalkerPaths.EnqueueDir(ctx, ph.DirHandle)
			// if err != nil {
			// 	jrpc2.ServerFromContext(ctx).Notify(ctx, "window/showMessage", &protocol.ShowMessageParams{
			// 		Type: protocol.Warning,
			// 		Message: fmt.Sprintf("Ignoring new folder %s: %s."+
			// 			" This is most likely bug, please report it.", rawURI, err),
			// 	})
			// 	return ids, err // continue
			// }
		} else {
			parentDir := filepath.Dir(rawPath)
			f.eventbus.DocumentChanged(eventbus.DocumentChangedEvent{
				Context: ctx,
				Path:    parentDir,
			})

			// ids = append(ids, jobIds...)
		}
	}

	return nil, nil
}

func (f *RootModulesFeature) handleModuleUriFromModuleLockFile(modUri string, changeType protocol.FileChangeType) (job.IDs, error) {
	var ids job.IDs

	modHandle := document.DirHandleFromURI(modUri)
	if changeType == protocol.Deleted {
		// This is unlikely to happen unless the user manually removed files
		// See https://github.com/hashicorp/terraform/issues/30005

		// err := svc.stateStore.Roots.UpdateModManifest(modHandle.Path(), nil, nil)
		err := f.UpdateModManifest(modHandle.Path(), nil, nil)
		if err != nil {
			f.logger.Printf("failed to remove module manifest for %q: %s", modHandle, err)
		}
		return ids, err
	}

	// TODO: figure out whether indexer methods stay
	// err := svc.indexModuleIfNotExists(ctx, modHandle)
	// if err != nil {
	// 	f.logger.Printf("failed to index module %q: %s", modHandle, err)
	// 	return ids, err
	// }

	// jobIds, err := svc.indexer.ModuleManifestChanged(ctx, modHandle)
	// if err != nil {
	// 	f.logger.Printf("error refreshing plugins for %q: %s", modHandle, err)
	// 	return ids, err
	// }
	// ids = append(ids, jobIds...)

	return ids, nil
}

func (f *RootModulesFeature) handleModuleUriFromPluginLockFile(ctx context.Context, changeType protocol.FileChangeType, modUri string) (job.IDs, error) {
	var ids job.IDs
	if changeType == protocol.Deleted {
		// This is unlikely to happen unless the user manually removed files
		// See https://github.com/hashicorp/terraform/issues/30005
		// Cached provider schema could be removed here but it may be useful
		// in other modules, so we trade some memory for better UX here.
		return nil, nil
	}

	// TODO: figure out whether indexer methods stay
	modHandle := document.DirHandleFromURI(modUri)
	// err := svc.indexModuleIfNotExists(ctx, modHandle)
	// if err != nil {
	// 	f.logger.Printf("failed to index module %q: %s", modHandle, err)
	// 	return nil, nil
	// }

	jobIds, err := f.pluginLockChanged(ctx, modHandle)
	if err != nil {
		f.logger.Printf("error refreshing plugins for %q: %s", modUri, err)
		return nil, nil
	}
	ids = append(ids, jobIds...)
	return ids, nil
}

func (f *RootModulesFeature) handleModuleUrifromDataDir(modUri string, changeType protocol.FileChangeType) (job.IDs, error) {
	// This is necessary because clients may not send delete notifications
	// for individual nested files when the parent directory is deleted.
	// VS Code / vscode-languageclient behaves this way.
	/*
		Contrary to (), the didChangeWatchedFiles event will not notify us about folders in VS Code

		https://github.com/microsoft/vscode/issues/90746 and https://github.com/microsoft/vscode/issues/60813 both describe that renaming or deleting the parent folder of a file that is watched will not raise an event. Commit https://github.com/microsoft/vscode/pull/139881/files#diff-0a75aed19c118603eb96332bc0b9c2d7867f4182346d16d18b7fc31b6ceeb321L10951-L10952 removed this explanation:

		> * *Note* that only files within the current [workspace folders](#workspace.workspaceFolders) can be watched.
		> * *Note* that when watching for file changes such as '**​/*.js', notifications will not be sent when a parent folder is
		> * moved or deleted (this is a known limitation of the current implementation and may change in the future).

		This means that this configuration: `fileEvents: [vscode.workspace.createFileSystemWatcher('**/ /*.ts')]` will only raise events for files, not folders. This means that `**/ /*.tf` will get create, changed, and delete events for all TF files individually, but not their parent folders. If something outside of VS Code deletes a folder with TF files inside, there will be no event raised for the folder deletion or the TF files. The files have to be deleted individually for the watcher to raise an event.

	We could use `fileEvents: [vscode.workspace.createFileSystemWatcher('**/ /*')]`, which causes all events for all files and all folders to be raised. However, more than just TF related files and folders will be raised, so we will get events for things that are not relevant. This makes using the client for events useless.

	To test out the behavior create an example folder with several TF files inside (this will activate the extension). Then create a subfolder with TF files inside that. Open VS Code with the TF extension enabled. Set VS Code logging to trace and open the developer tools console, and filter to see only file watcher events (instructions at https://github.com/microsoft/vscode/wiki/File-Watcher-Issues#logging-local). Using another shell outside of VS Code, delete the subfolder recursively. Note in the logs that VS Code's filewatcher sees the deletion of the folder and all files within, but does not send any events.

	Repeat the above, but this time use `fileEvents: [vscode.workspace.createFileSystemWatcher('**/ /*')]`. Note in the logs that VS Code's filewatcher sees the deletion of the folder and all files within, and sends a notification for the folder and all files.
	 */
	modHandle := document.DirHandleFromURI(modUri)
	if changeType == protocol.Deleted {
		// This is unlikely to happen unless the user manually removed files
		// See https://github.com/hashicorp/terraform/issues/30005
		// err := svc.stateStore.Roots.UpdateModManifest(modHandle.Path(), nil, nil)
		err := f.UpdateModManifest(modHandle.Path(), nil, nil)
		if err != nil {
			f.logger.Printf("failed to remove module manifest for %q: %s", modHandle, err)
			return nil, err
		}
	}

	return nil, nil
}

type parsedModuleHandle struct {
	DirHandle document.DirHandle
	IsDir     bool
}

var ErrorSkip = errSkip{}

type errSkip struct{}

func (es errSkip) Error() string {
	return "skip"
}

func modHandleFromRawOsPath(ctx context.Context, rawPath string) (*parsedModuleHandle, error) {
	fi, err := os.Stat(rawPath)
	if err != nil {
		return nil, err
	}

	// URI can either be a file or a directory based on the LSP spec.
	if fi.IsDir() {
		return &parsedModuleHandle{
			DirHandle: document.DirHandleFromPath(rawPath),
			IsDir:     true,
		}, nil
	}

	// TODO
	if !ast.IsModuleFilename(fi.Name()) && !varast.IsVarsFilename(fi.Name()) {
		jrpc2.ServerFromContext(ctx).Notify(ctx, "window/showMessage", &protocol.ShowMessageParams{
			Type: protocol.Warning,
			Message: fmt.Sprintf("Unable to update %q: filetype not supported. "+
				"This is likely a bug which should be reported.", rawPath),
		})
		return nil, ErrorSkip
	}

	docHandle := document.HandleFromPath(rawPath)
	return &parsedModuleHandle{
		DirHandle: docHandle.Dir,
		IsDir:     false,
	}, nil
}

func (f *RootModulesFeature) removeIndexedModule(ctx context.Context, modURI string) {
	// TODO: we don't index anymore...is this needed?

	// modHandle := document.DirHandleFromURI(modURI)

	// err := svc.stateStore.WalkerPaths.DequeueDir(modHandle)
	// if err != nil {
	// 	jrpc2.ServerFromContext(ctx).Notify(ctx, "window/showMessage", &lsp.ShowMessageParams{
	// 		Type: lsp.Warning,
	// 		Message: fmt.Sprintf("Ignoring removed folder %s: %s."+
	// 			" This is most likely bug, please report it.", modURI, err),
	// 	})
	// 	return
	// }

	// err = svc.stateStore.JobStore.DequeueJobsForDir(modHandle)
	// if err != nil {
	// 	svc.logger.Printf("failed to dequeue jobs for module: %s", err)
	// 	return
	// }

	// callers, err := f.store.CallersOfModule(modHandle.Path())
	// if err != nil {
	// 	f.logger.Printf("failed to remove module from watcher: %s", err)
	// 	return
	// }

	// if len(callers) == 0 {
	// 	err = f.store.Remove(modHandle.Path())
	// 	f.logger.Printf("failed to remove records: %s", err)
	// }
}

func (f *RootModulesFeature) indexModuleIfNotExists(ctx context.Context, modHandle document.DirHandle) error {
	// _, err := svc.modStore.ModuleByPath(modHandle.Path())
	// if err != nil {
	// 	if state.IsModuleNotFound(err) {
	// 		err = svc.stateStore.WalkerPaths.EnqueueDir(ctx, modHandle)
	// 		if err != nil {
	// 			return fmt.Errorf("failed to walk module %q: %w", modHandle, err)
	// 		}
	// 		err = svc.stateStore.WalkerPaths.WaitForDirs(ctx, []document.DirHandle{modHandle})
	// 		if err != nil {
	// 			return fmt.Errorf("failed to wait for module walk %q: %w", modHandle, err)
	// 		}
	// 	} else {
	// 		return fmt.Errorf("failed to find module %q: %w", modHandle, err)
	// 	}
	// }

	return nil
}
