<?php

namespace App\Agent\Exceptions;

/** The agent couldn't be reached, or stopped answering mid-reply. */
final class AgentUnavailable extends AgentException {}
