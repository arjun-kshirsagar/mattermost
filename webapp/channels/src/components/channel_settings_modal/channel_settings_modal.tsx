// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React, {
    useState,
    useRef,
    useEffect,
    useCallback,
} from 'react';
import {FormattedMessage, useIntl} from 'react-intl';
import {useDispatch, useSelector} from 'react-redux';

import {GenericModal} from '@mattermost/components';
import type {Channel, ChannelType} from '@mattermost/types/channels';
import type {ServerError} from '@mattermost/types/errors';

import {patchChannel} from 'mattermost-redux/actions/channels';
import Permissions from 'mattermost-redux/constants/permissions';
import {haveITeamPermission} from 'mattermost-redux/selectors/entities/roles';

import {setShowPreviewOnChannelSettingsModal} from 'actions/views/textbox';
import {showPreviewOnChannelSettingsModal} from 'selectors/views/textbox';

import ChannelNameFormField from 'components/channel_name_form_field/channel_name_form_field';
import ConfirmationModal from 'components/confirm_modal';
import Textbox, {TextboxLinks} from 'components/textbox';
import type {TextboxElement} from 'components/textbox';
import type TextboxClass from 'components/textbox/textbox';
import SaveChangesPanel, {type SaveChangesPanelState} from 'components/widgets/modals/components/save_changes_panel';
import PublicPrivateSelector from 'components/widgets/public-private-selector/public-private-selector';

import {focusElement} from 'utils/a11y_utils';
import Constants from 'utils/constants';
import {isKeyPressed, cmdOrCtrlPressed} from 'utils/keyboard';
import {stopTryNotificationRing} from 'utils/notification_sounds';

import type {GlobalState} from 'types/store';

import './channel_settings_modal.scss';

// Lazy-loaded components
const SettingsSidebar = React.lazy(() => import('components/settings_sidebar'));

type ChannelSettingsModalProps = {
    channel: Channel;
    onExited: () => void;
    focusOriginElement?: string;
    isOpen: boolean;
};

enum ChannelSettingsTabs {
    INFO = 'info',
    CONFIGURATION = 'configuration',
    ARCHIVE = 'archive',
}

function ChannelSettingsModal({channel, isOpen, onExited, focusOriginElement}: ChannelSettingsModalProps) {
    const {formatMessage} = useIntl();
    const dispatch = useDispatch();
    const shouldShowPreview = useSelector(showPreviewOnChannelSettingsModal);

    const canConvertToPrivate = useSelector((state: GlobalState) =>
        haveITeamPermission(state, channel?.team_id ?? '', Permissions.CREATE_PRIVATE_CHANNEL),
    );
    const canConvertToPublic = useSelector((state: GlobalState) =>
        haveITeamPermission(state, channel?.team_id ?? '', Permissions.CREATE_PUBLIC_CHANNEL),
    );

    const [show, setShow] = useState(isOpen);

    // Active tab
    const [activeTab, setActiveTab] = useState<ChannelSettingsTabs>(ChannelSettingsTabs.INFO);

    // We track unsaved changes to prompt a save changes panel
    const [requireConfirm, setRequireConfirm] = useState(false);
    const [showArchiveConfirmModal, setShowArchiveConfirmModal] = useState(false);
    const [saveChangesPanelState, setSaveChangesPanelState] = useState<SaveChangesPanelState>();

    // The fields we allow editing
    const [displayName, setDisplayName] = useState(channel?.display_name ?? '');
    const [url, setURL] = useState(channel?.name ?? '');
    const [channelPurpose, setChannelPurpose] = useState(channel.purpose ?? '');

    const [header, setChannelHeader] = useState(channel?.header ?? '');
    const [channelType, setChannelType] = useState<ChannelType>(channel?.type as ChannelType ?? Constants.OPEN_CHANNEL as ChannelType);

    // Refs
    const modalBodyRef = useRef<HTMLDivElement>(null);
    const headerTextboxRef = useRef<TextboxClass>(null);

    // Constants
    const headerMaxLength = 1024;

    // UI Feedback: errors, states
    const [urlError, setURLError] = useState('');
    const [serverError, setServerError] = useState('');

    // For checking unsaved changes, we store the initial "loaded" values or do a direct comparison
    const hasUnsavedChanges = useCallback(() => {
        // Compare fields to their original values
        if (!channel) {
            return false;
        }
        return (
            displayName !== channel.display_name ||
            url !== channel.name ||
            channelPurpose !== channel.purpose ||
            header !== channel.header ||
            channelType !== channel.type
        );
    }, [channel, displayName, url, channelPurpose, header, channelType]);

    // Possibly set requireConfirm whenever an edit has occurred
    useEffect(() => {
        setRequireConfirm(hasUnsavedChanges());
    }, [displayName, url, channelPurpose, header, channelType, hasUnsavedChanges]);

    // For KeyDown handling
    useEffect(() => {
        const handleKeyDown = (e: KeyboardEvent) => {
            if (cmdOrCtrlPressed(e) && e.shiftKey && isKeyPressed(e, Constants.KeyCodes.A)) {
                e.preventDefault();
                handleHide();
            }
        };
        document.addEventListener('keydown', handleKeyDown);
        return () => document.removeEventListener('keydown', handleKeyDown);
    }, []);

    // Called to set the active tab, prompting save changes panel if there are unsaved changes
    const updateTab = useCallback((newTab: string) => {
        if (requireConfirm) {
            setSaveChangesPanelState('editing');
            return;
        }
        updateTabConfirm(newTab);
    }, [requireConfirm]);

    const updateTabConfirm = (newTab: string) => {
        const tab = newTab as ChannelSettingsTabs;
        setActiveTab(tab);

        if (modalBodyRef.current) {
            modalBodyRef.current.scrollTop = 0;
        }
    };

    // Handle save changes panel actions
    const handleSaveChanges = useCallback(async () => {
        const success = await handleSave();
        if (!success) {
            setSaveChangesPanelState('error');
            return;
        }
        setSaveChangesPanelState('saved');

        // Don't set requireConfirm to false here
        // Let the SaveChangesPanel's automatic timeout handle it
    }, []);

    const handleClose = useCallback(() => {
        setSaveChangesPanelState(undefined);
        setRequireConfirm(false);
    }, []);

    const handleCancel = useCallback(() => {
        // Reset all form fields to their original values
        setDisplayName(channel?.display_name ?? '');
        setURL(channel?.name ?? '');
        setChannelPurpose(channel?.purpose ?? '');
        setChannelHeader(channel?.header ?? '');
        setChannelType(channel?.type as ChannelType ?? Constants.OPEN_CHANNEL as ChannelType);

        // Clear errors
        setURLError('');
        setServerError('');

        handleClose();
    }, [channel]);

    const handleHide = () => {
        if (requireConfirm) {
            setSaveChangesPanelState('editing');
        } else {
            handleHideConfirm();
        }
    };

    const handleHideConfirm = () => {
        stopTryNotificationRing();
        setShow(false);
    };

    // Called after the fade-out completes
    const handleHidden = () => {
        // Clear anything if needed
        setActiveTab(ChannelSettingsTabs.INFO);
        if (focusOriginElement) {
            focusElement(focusOriginElement, true);
        }
        onExited();
    };

    // Validate & Save
    const handleSave = async (): Promise<boolean> => {
        if (!channel) {
            return false;
        }

        // TODO: expand this simple example of the client-side check, enhance and cover all scenarios shown int he UX
        if (!displayName.trim()) {
            setServerError(formatMessage({
                id: 'channel_settings.error_display_name_required',
                defaultMessage: 'Channel name is required',
            }));
            return false;
        }

        // Build updated channel object
        const updated: Channel = {
            ...channel,
            display_name: displayName.trim(),
            name: url.trim(),
            purpose: channelPurpose.trim(),
            header: header.trim(),
            type: channelType as ChannelType,
        };

        const {error} = await dispatch(patchChannel(channel.id, updated));
        if (error) {
            handleServerError(error as ServerError);
            return false;
        }

        // Return success, but don't close the modal yet
        // Let the SaveChangesPanel show the "Settings saved" message first
        return true;
    };

    const handleServerError = (err: ServerError) => {
        setServerError(err.message || formatMessage({id: 'channel_settings.unknown_error', defaultMessage: 'Something went wrong.'}));
    };

    // Example of toggling from open <-> private
    const handleChannelTypeChange = (type: ChannelType) => {
        // If canCreatePublic is false, do not allow. Similarly if canCreatePrivate is false, do not allow
        if (type === Constants.OPEN_CHANNEL && !canConvertToPublic) {
            return;
        }
        if (type === Constants.PRIVATE_CHANNEL && !canConvertToPrivate) {
            return;
        }
        setChannelType(type);
        setServerError('');
    };

    const handleArchiveChannel = () => {
        setShowArchiveConfirmModal(true);
    };

    const doArchiveChannel = () => {
        // TODO: add the extra logic to archive the channel
        handleHideConfirm();
    };

    // Renders content based on active tab
    const renderTabContent = () => {
        switch (activeTab) {
        case ChannelSettingsTabs.INFO:
            return renderInfoTab();
        case ChannelSettingsTabs.CONFIGURATION:
            return renderConfigurationTab();
        case ChannelSettingsTabs.ARCHIVE:
            return renderArchiveTab();
        default:
            return renderInfoTab();
        }
    };

    const handleURLChange = useCallback((newURL: string) => {
        setURL(newURL);
        setURLError('');
    }, []);

    const handleKeyPress = () => {
        return true;
    };

    const handleOnChange = (e: React.ChangeEvent<TextboxElement>) => {
        setChannelHeader(e.target.value);

        // Check for character limit
        if (e.target.value.length > headerMaxLength) {
            setServerError(formatMessage({
                id: 'edit_channel_header_modal.error',
                defaultMessage: 'The text entered exceeds the character limit. The channel header is limited to {maxLength} characters.',
            }, {
                maxLength: headerMaxLength,
            }));
        } else if (serverError) {
            setServerError('');
        }
    };

    const renderInfoTab = () => {
        // Channel name, URL, purpose, header, plus the public/private toggle
        return (
            <div className='ChannelSettingsModal__infoTab'>
                <label className='Input_legend'>{formatMessage({id: 'channel_settings.label.name', defaultMessage: 'Channel Name'})}</label>
                <ChannelNameFormField
                    value={displayName}
                    name='channel-settings-name'
                    placeholder={formatMessage({
                        id: 'channel_settings_modal.name.placeholder',
                        defaultMessage: 'Enter a name for your channel',
                    })}
                    onDisplayNameChange={(name) => {
                        setDisplayName(name);
                    }}
                    onURLChange={handleURLChange}
                    urlError={urlError}
                    currentUrl={channel.name}
                />

                <PublicPrivateSelector
                    className='ChannelSettingsModal__typeSelector'
                    selected={channelType}
                    publicButtonProps={{
                        title: formatMessage({id: 'channel_modal.type.public.title', defaultMessage: 'Public Channel'}),
                        description: formatMessage({id: 'channel_modal.type.public.description', defaultMessage: 'Anyone can join'}),
                        disabled: !canConvertToPublic,
                    }}
                    privateButtonProps={{
                        title: formatMessage({id: 'channel_modal.type.private.title', defaultMessage: 'Private Channel'}),
                        description: formatMessage({id: 'channel_modal.type.private.description', defaultMessage: 'Only invited members'}),
                        disabled: !canConvertToPrivate,
                    }}
                    onChange={handleChannelTypeChange}
                />

                {/* Purpose Section*/}
                {/* <label className='Input_legend'>{formatMessage({id: 'channel_settings.label.purpose', defaultMessage: 'Channel Purpose'})}</label> */}
                {/* <div className='textarea-wrapper'>
                    <Textbox
                        value={channelPurpose}
                        onChange={(e: React.ChangeEvent<TextboxElement>) => {
                            setChannelPurpose(e.target.value);

                            // Check for character limit
                            if (e.target.value.length > Constants.MAX_CHANNELPURPOSE_LENGTH) {
                                setServerError(formatMessage({
                                    id: 'channel_settings.error_purpose_length',
                                    defaultMessage: 'The text entered exceeds the character limit. The channel purpose is limited to {maxLength} characters.',
                                }, {
                                    maxLength: Constants.MAX_CHANNELPURPOSE_LENGTH,
                                }));
                            } else if (serverError) {
                                setServerError('');
                            }
                        }}
                        onKeyPress={() => {
                            // No specific key press handling needed for the settings modal
                        }}
                        onKeyDown={() => {
                            // No specific key down handling needed for the settings modal
                        }}
                        supportsCommands={false}
                        suggestionListPosition='bottom'
                        createMessage={formatMessage({
                            id: 'channel_settings_modal.purpose.placeholder',
                            defaultMessage: 'Enter a purpose for this channel (optional)',
                        })}
                        handlePostError={() => {
                            // No specific post error handling needed for the settings modal
                        }}
                        channelId={channel.id}
                        id='channel_settings_purpose_textbox'
                        characterLimit={Constants.MAX_CHANNELPURPOSE_LENGTH}
                        preview={shouldShowPreview}
                        useChannelMentions={false}
                    />
                </div>
                <div className='post-create-footer'>
                    <TextboxLinks
                        showPreview={shouldShowPreview}
                        updatePreview={(show) => {
                            dispatch(setShowPreviewOnChannelSettingsModal(show));
                        }}
                        hasText={channelPurpose ? channelPurpose.length > 0 : false}
                        hasExceededCharacterLimit={channelPurpose ? channelPurpose.length > Constants.MAX_CHANNELPURPOSE_LENGTH : false}
                        previewMessageLink={
                            <FormattedMessage
                                id='edit_channel_purpose_modal.previewPurpose'
                                defaultMessage='Edit'
                            />
                        }
                    />
                </div> */}
                {/* Channel Header Section*/}
                <label className='Input_legend'>{formatMessage({id: 'channel_settings.label.header', defaultMessage: 'Channel Header'})}</label>
                <div className='textarea-wrapper'>
                    <Textbox
                        value={header!}
                        onChange={handleOnChange}
                        supportsCommands={false}
                        suggestionListPosition='bottom'
                        createMessage={formatMessage({
                            id: 'channel_settings_modal.header.placeholder',
                            defaultMessage: 'Enter a header for this channel',
                        })}
                        channelId={channel.id!}
                        id='edit_textbox'
                        ref={headerTextboxRef}
                        characterLimit={headerMaxLength}
                        preview={true}
                        useChannelMentions={false}
                        onKeyPress={handleKeyPress}
                    />
                </div>
                <div className='post-create-footer'>
                    <TextboxLinks
                        showPreview={shouldShowPreview}
                        updatePreview={(show) => {
                            dispatch(setShowPreviewOnChannelSettingsModal(show));
                        }}
                        hasText={header ? header.length > 0 : false}
                        hasExceededCharacterLimit={header ? header.length > headerMaxLength : false}
                        previewMessageLink={
                            <FormattedMessage
                                id='edit_channel_header_modal.previewHeader'
                                defaultMessage='Edit'
                            />
                        }
                    />
                </div>
                {/* SaveChangesPanel for unsaved changes */}
                {requireConfirm && (
                    <SaveChangesPanel
                        handleSubmit={handleSaveChanges}
                        handleCancel={handleCancel}
                        handleClose={handleClose}
                        tabChangeError={false}
                        state={saveChangesPanelState}
                    />
                )}
            </div>
        );
    };

    const renderConfigurationTab = () => {
        // Could show channel permissions, guest/member permissions, etc.
        return (
            <div className='ChannelSettingsModal__configurationTab'>
                <FormattedMessage
                    id='channel_settings.configuration.placeholder'
                    defaultMessage='Channel Permissions or Additional Configuration (WIP)'
                />
            </div>
        );
    };

    const renderArchiveTab = () => {
        return (
            <div className='ChannelSettingsModal__archiveTab'>
                <FormattedMessage
                    id='channel_settings.archive.warning'
                    defaultMessage='Archiving this channel will remove it from the channel list. Are you sure you want to proceed?'
                />
                <button
                    type='button'
                    className='btn btn-danger'
                    onClick={handleArchiveChannel}
                    id='channelSettingsArchiveChannelButton'
                >
                    <FormattedMessage
                        id='channel_settings.archive.button'
                        defaultMessage='Archive Channel'
                    />
                </button>
            </div>
        );
    };

    // Define tabs for the settings sidebar
    const tabs = [
        {
            name: ChannelSettingsTabs.INFO,
            uiName: formatMessage({id: 'channel_settings.tab.info', defaultMessage: 'Info'}),
            icon: 'icon icon-information-outline',
            iconTitle: formatMessage({id: 'generic_icons.info', defaultMessage: 'Info Icon'}),
        },
        {
            name: ChannelSettingsTabs.CONFIGURATION,
            uiName: formatMessage({id: 'channel_settings.tab.configuration', defaultMessage: 'Configuration'}),
            icon: 'icon icon-cog-outline',
            iconTitle: formatMessage({id: 'generic_icons.settings', defaultMessage: 'Settings Icon'}),
        },
        {
            name: ChannelSettingsTabs.ARCHIVE,
            uiName: formatMessage({id: 'channel_settings.tab.archive', defaultMessage: 'Archive Channel'}),
            icon: 'icon icon-archive-outline',
            iconTitle: formatMessage({id: 'generic_icons.archive', defaultMessage: 'Archive Icon'}),
            newGroup: true,
        },
    ];

    // Renders the body: left sidebar for tabs, the content on the right
    const renderModalBody = () => {
        return (
            <div
                ref={modalBodyRef}
                className='ChannelSettingsModal__bodyWrapper'
            >
                <div className='settings-table'>
                    <div className='settings-links'>
                        <React.Suspense fallback={null}>
                            <SettingsSidebar
                                tabs={tabs}
                                activeTab={activeTab}
                                updateTab={updateTab}
                            />
                        </React.Suspense>
                    </div>
                    <div className='settings-content minimize-settings'>
                        {renderTabContent()}
                    </div>
                </div>
            </div>
        );
    };

    // For the main modal heading
    const modalTitle = formatMessage({id: 'channel_settings.modal.title', defaultMessage: 'Channel Settings'});

    // Error text to show in the GenericModal "footer area"
    const errorText = serverError;

    return (
        <GenericModal
            id='channelSettingsModal'
            ariaLabel={modalTitle}
            className='ChannelSettingsModal settings-modal'
            show={show}
            onHide={handleHide}
            onExited={handleHidden}
            compassDesign={true}

            // The main heading:
            modalHeaderText={modalTitle}
            errorText={errorText}

            // If pressing Enter in a sub‐form, we also want to handle saving:
            handleEnterKeyPress={handleSaveChanges}
            bodyPadding={false}
        >
            {renderModalBody()}

            {/* Confirmation Modal for archiving channel */}
            {showArchiveConfirmModal &&
                <ConfirmationModal
                    id='archiveChannelConfirmModal'
                    show={true}
                    title={formatMessage({id: 'channel_settings.modal.archiveTitle', defaultMessage: 'Archive Channel?'})}
                    message={formatMessage({id: 'channel_settings.modal.archiveMsg', defaultMessage: 'Are you sure you want to archive this channel? This action cannot be undone.'})}
                    confirmButtonText={formatMessage({id: 'channel_settings.modal.confirmArchive', defaultMessage: 'Yes, Archive'})}
                    onConfirm={doArchiveChannel}
                    onCancel={() => setShowArchiveConfirmModal(false)}
                    confirmButtonClass='btn-danger'
                    modalClass='archiveChannelConfirmModal'
                    focusOriginElement='channelSettingsArchiveChannelButton'
                />
            }
        </GenericModal>
    );
}

export default ChannelSettingsModal;
